package ffmpeg

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/easypodcasts/go-worker/internal/build"
)

type Progress struct {
	FramesProcessed string
	CurrentTime     string
	CurrentBitrate  string
	Progress        int
	Speed           string
}

// Transcoder Main struct
type Transcoder struct {
	stdErrPipe io.ReadCloser
	stdInPipe  io.WriteCloser

	process       *exec.Cmd
	inputMedia    string
	inputDuration float64
}

func NewTranscoder(inputMedia string) *Transcoder {
	return &Transcoder{inputMedia: inputMedia}
}

// GetCommand Build and get command
func (t Transcoder) GetCommand() []string {
	args := []string{
		"-y",
		"-user_agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/95.0.4638.54 Safari/537.36",
		"-i",
		t.inputMedia,
		"-vn",
		"-ac", "1",
		"-c:a", "libopus",
		"-b:a", "24k",
		"-vbr", "on",
		"-compression_level", "10",
		"-frame_duration", "60",
		"-application", "voip",
		"-metadata", "Description=Easypodcast",
		"-metadata", fmt.Sprintf("encoder_version=\"easypodcast-go %s\"", build.Version),
		"episode.mp4",
	}
	return args
}

// Run Starts the transcoding process
func (t *Transcoder) Run(dir string, progress bool) <-chan error {
	done := make(chan error)
	command := t.GetCommand()

	if !progress {
		command = append([]string{"-nostats", "-loglevel", "0"}, command...)
	}

	proc := exec.Command("ffmpeg", command...)
	if progress {
		errStream, err := proc.StderrPipe()
		if err != nil {
			fmt.Println("Progress not available: " + err.Error())
		} else {
			t.stdErrPipe = errStream
		}
	}
	proc.Dir = dir

	// Set the stdinPipe in case we need to stop the transcoding
	stdin, err := proc.StdinPipe()
	if nil != err {
		fmt.Println("Stdin not available: " + err.Error())
	}
	t.stdInPipe = stdin

	// If the user has requested progress, we send it to them on a Buffer
	var outBuffer bytes.Buffer
	if progress {
		proc.Stdout = &outBuffer
	}

	err = proc.Start()

	t.process = proc

	go func(err error) {
		if err != nil {
			done <- fmt.Errorf("failed to start ffmpeg (%s) with %s, message %s", command, err, outBuffer.String())
			close(done)
			return
		}

		err = proc.Wait()

		if err != nil {
			err = fmt.Errorf("failed to start ffmpeg (%s) with %s, message %s", command, err, outBuffer.String())
		}
		done <- err
		close(done)
	}(err)

	return done
}

// Stop Ends the transcoding process
func (t *Transcoder) Stop() error {
	if t.process == nil {
		return nil
	}

	stdin := t.stdInPipe
	if stdin != nil {
		_, err := stdin.Write([]byte("q\n"))
		if err != nil {
			return err
		}
		return nil
	}

	err := t.process.Process.Signal(syscall.SIGTERM)
	if err != nil {
		return err
	}
	return nil
}

// Output Returns the transcoding progress channel
func (t *Transcoder) Output() <-chan Progress {
	out := make(chan Progress)

	go func() {
		defer close(out)
		if t.stdErrPipe == nil {
			out <- Progress{}
			return
		}

		defer func(stdErrPipe io.ReadCloser) {
			_ = stdErrPipe.Close()
		}(t.stdErrPipe)

		scanner := bufio.NewScanner(t.stdErrPipe)
		split := func(data []byte, atEOF bool) (advance int, token []byte, spliterror error) {
			if atEOF && len(data) == 0 {
				return 0, nil, nil
			}
			//windows \r\n
			//so  first \r and then \n can remove unexpected line break
			if i := bytes.IndexByte(data, '\r'); i >= 0 {
				// We have a cr terminated line
				return i + 1, data[0:i], nil
			}
			if i := bytes.IndexByte(data, '\n'); i >= 0 {
				// We have a full newline-terminated line.
				return i + 1, data[0:i], nil
			}
			if atEOF {
				return len(data), data, nil
			}

			return 0, nil, nil
		}
		buf := make([]byte, 2)
		scanner.Buffer(buf, bufio.MaxScanTokenSize)
		scanner.Split(split)

		for scanner.Scan() {
			line := scanner.Text()

			var re = regexp.MustCompile(`=\s+`)
			st := strings.ReplaceAll(re.ReplaceAllString(line, `=`), ",", "")
			fields := strings.Fields(st)

			switch {
			case strings.Contains(line, "Duration:"):
				t.inputDuration = durToSec(fields[1])
			case strings.Contains(line, "speed=") &&
				strings.Contains(line, "time=") &&
				strings.Contains(line, "bitrate="):

				var progress Progress

				var (
					framesProcessed string
					currentTime     string
					currentBitrate  string
					currentSpeed    string
				)

				for j := 0; j < len(fields); j++ {
					field := fields[j]
					fieldSplit := strings.Split(field, "=")

					if len(fieldSplit) > 1 {
						fieldName := fieldSplit[0]
						fieldValue := fieldSplit[1]

						switch fieldName {
						case "frame":
							framesProcessed = fieldValue
						case "time":
							currentTime = fieldValue
						case "bitrate":
							currentBitrate = fieldValue
						case "speed":
							currentSpeed = fieldValue
						}
					}
				}

				timeSec := durToSec(currentTime)
				if t.inputDuration != 0 {
					// Progress calculation
					percent := (timeSec * 100) / t.inputDuration
					progress.Progress = int(math.Round(percent))
				}
				progress.CurrentBitrate = currentBitrate
				progress.FramesProcessed = framesProcessed
				progress.CurrentTime = currentTime
				progress.Speed = currentSpeed
				out <- progress

			}
		}
	}()

	return out
}

func durToSec(dur string) float64 {
	durAry := strings.Split(dur, ":")
	var secs float64
	if len(durAry) != 3 {
		return 0
	}
	hr, _ := strconv.ParseFloat(durAry[0], 64)
	secs = hr * (60 * 60)
	min, _ := strconv.ParseFloat(durAry[1], 64)
	secs += min * (60)
	second, _ := strconv.ParseFloat(durAry[2], 64)
	secs += second
	return secs
}
