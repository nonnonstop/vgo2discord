package main

import (
	"os"
	"os/signal"

	"github.com/bwmarrin/discordgo"
	"github.com/gordonklaus/portaudio"
	"github.com/sirupsen/logrus"
	"golang.org/x/sys/windows"
	"gopkg.in/yaml.v2"
	"layeh.com/gopus"
)

func InitProcess() error {
	return windows.SetPriorityClass(windows.CurrentProcess(), 0x00000080)
}

type MainConfig struct {
	Discord struct {
		Token   string `yaml:"token"`
		Guild   string `yaml:"guild"`
		Channel string `yaml:"channel"`
	} `yaml:"discord"`
}

func (config *MainConfig) Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	decoder := yaml.NewDecoder(f)
	return decoder.Decode(&config)
}

type Discord struct {
	session *discordgo.Session
	voice   *discordgo.VoiceConnection
}

func (discord *Discord) Start(token string) error {
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return err
	}

	if err := session.Open(); err != nil {
		session.Close()
		return err
	}

	discord.session = session
	return nil
}

func (discord *Discord) Stop() error {
	return discord.session.Close()
}

func (discord *Discord) JoinVoiceChannel(groupId, channelId string) error {
	voice, err := discord.session.ChannelVoiceJoin(groupId, channelId, false, true)
	if err != nil {
		return err
	}
	discord.voice = voice
	return nil
}

func (discord *Discord) LeaveVoiceChannel() error {
	voice := discord.voice
	if voice == nil {
		return nil
	}
	return voice.Disconnect()
}

func (discord *Discord) SendVoice(opus []byte) bool {
	voice := discord.voice
	if voice == nil {
		return false
	}
	if voice.Ready == false {
		return false
	}
	if voice.OpusSend == nil {
		return false
	}
	voice.OpusSend <- opus
	return true
}

func (discord *Discord) Speaking(speaking bool) bool {
	voice := discord.voice
	if voice == nil {
		return false
	}
	if voice.Ready == false {
		return false
	}
	voice.Speaking(speaking)
	return true
}

type Audio struct {
	stream *portaudio.Stream
}

func (audio *Audio) Start() error {
	return portaudio.Initialize()
}

func (audio *Audio) Stop() error {
	return portaudio.Terminate()
}

func (audio *Audio) Open(discord *Discord, errCh chan<- error) error {
	in := make([]int16, 960*2)
	stream, err := portaudio.OpenDefaultStream(2, 0, 48000, len(in), in)
	if err != nil {
		return err
	}
	if err := stream.Start(); err != nil {
		stream.Close()
		return err
	}
	audio.stream = stream
	go func() {
		opusEncoder, err := gopus.NewEncoder(48000, 2, gopus.Audio)
		speaked := true
		if err != nil {
			errCh <- err
			return
		}
		for {
			if err := stream.Read(); err != nil {
				errCh <- err
				if err == portaudio.InputOverflowed {
					continue
				}
				return
			}
			speaking := true
			for _, value := range in {
				if value > 1 || value < -1 {
					speaking = false
					break
				}
			}
			if speaking {
				if speaked {
					speaked = false
					go discord.Speaking(false)
				}
				continue
			}
			if !speaked {
				speaked = true
			}
			opus, err := opusEncoder.Encode(in, 960, 960*2*2)
			if err != nil {
				errCh <- err
				return
			}
			discord.SendVoice(opus)
		}
	}()
	return nil
}

func (audio *Audio) Close() error {
	if err := audio.stream.Stop(); err != nil {
		audio.stream.Close()
		return err
	}
	if err := audio.stream.Close(); err != nil {
		return err
	}
	return nil
}

func main() {
	// Configure logging
	log := logrus.New()
	log.Out = os.Stdout

	errCh := make(chan error)
	defer close(errCh)
	go func() {
		for err := range errCh {
			log.Error(err)
		}
	}()

	// Init process
	log.Debug("Initialize process")
	if err := InitProcess(); err != nil {
		log.Error("failed to init process:", err)
		return
	}

	// Config
	log.Debug("Load config file")
	var mainConfig MainConfig
	if err := mainConfig.Load("config.yml"); err != nil {
		log.Error("failed to load config:", err)
		return
	}

	// Discord
	log.Debug("Login to Discord")
	var discord Discord
	if err := discord.Start(mainConfig.Discord.Token); err != nil {
		log.Error("failed to start discord client:", err)
		return
	}
	defer discord.Stop()
	if err := discord.JoinVoiceChannel(mainConfig.Discord.Guild, mainConfig.Discord.Channel); err != nil {
		log.Error("failed to join voice channel:", err)
		return
	}
	defer discord.LeaveVoiceChannel()

	// Sound device
	log.Debug("Capture audio device")
	var audio Audio
	if err := audio.Start(); err != nil {
		log.Error("failed to start audio:", err)
		return
	}
	defer audio.Stop()
	if err := audio.Open(&discord, errCh); err != nil {
		log.Error("failed to start audio device:", err)
		return
	}
	defer audio.Close()

	// Wait
	log.Info("Running... Press Ctrl+C to quit")
	stop := make(chan os.Signal)
	defer close(stop)
	signal.Notify(stop, os.Interrupt)
	<-stop
}
