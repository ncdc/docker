package daemon

import (
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/docker/docker/engine"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/docker/docker/pkg/jsonlog"
	"github.com/docker/docker/pkg/log"
	"github.com/docker/docker/pkg/promise"
	"github.com/docker/docker/utils"
)

func (daemon *Daemon) ContainerAttach(job *engine.Job) engine.Status {
	if len(job.Args) != 1 {
		return job.Errorf("Usage: %s CONTAINER\n", job.Name)
	}

	var (
		name   = job.Args[0]
		logs   = job.GetenvBool("logs")
		stream = job.GetenvBool("stream")
		stdin  = job.GetenvBool("stdin")
		stdout = job.GetenvBool("stdout")
		stderr = job.GetenvBool("stderr")
	)

	container := daemon.Get(name)
	if container == nil {
		return job.Errorf("No such container: %s", name)
	}

	//logs
	if logs {
		cLog, err := container.ReadLog("json")
		if err != nil && os.IsNotExist(err) {
			// Legacy logs
			log.Debugf("Old logs format")
			if stdout {
				cLog, err := container.ReadLog("stdout")
				if err != nil {
					log.Errorf("Error reading logs (stdout): %s", err)
				} else if _, err := io.Copy(job.Stdout, cLog); err != nil {
					log.Errorf("Error streaming logs (stdout): %s", err)
				}
			}
			if stderr {
				cLog, err := container.ReadLog("stderr")
				if err != nil {
					log.Errorf("Error reading logs (stderr): %s", err)
				} else if _, err := io.Copy(job.Stderr, cLog); err != nil {
					log.Errorf("Error streaming logs (stderr): %s", err)
				}
			}
		} else if err != nil {
			log.Errorf("Error reading logs (json): %s", err)
		} else {
			dec := json.NewDecoder(cLog)
			for {
				l := &jsonlog.JSONLog{}

				if err := dec.Decode(l); err == io.EOF {
					break
				} else if err != nil {
					log.Errorf("Error streaming logs: %s", err)
					break
				}
				if l.Stream == "stdout" && stdout {
					io.WriteString(job.Stdout, l.Log)
				}
				if l.Stream == "stderr" && stderr {
					io.WriteString(job.Stderr, l.Log)
				}
			}
		}
	}

	//stream
	if stream {
		var (
			cStdin           io.ReadCloser
			cStdout, cStderr io.Writer
		)

		if stdin {
			r, w := io.Pipe()
			go func() {
				defer w.Close()
				defer log.Debugf("Closing buffered stdin pipe")
				io.Copy(w, job.Stdin)
			}()
			cStdin = r
		}
		if stdout {
			cStdout = job.Stdout
		}
		if stderr {
			cStderr = job.Stderr
		}

		<-daemon.attach(&container.StreamConfig, container.Config.OpenStdin, container.Config.StdinOnce, container.Config.Tty, cStdin, cStdout, cStderr)
		// If we are in stdinonce mode, wait for the process to end
		// otherwise, simply return
		if container.Config.StdinOnce && !container.Config.Tty {
			container.WaitStop(-1 * time.Second)
		}
	}
	return engine.StatusOK
}

type streamType int

const streamStdin streamType = 0
const streamStdout streamType = 1
const streamStderr streamType = 2

func (s streamType) String() string {
	switch s {
	case streamStdin:
		return "stdin"
	case streamStdout:
		return "stdout"
	case streamStderr:
		return "stderr"
	}
	return "<unknown>"
}

type attachResult struct {
	err    error
	stream streamType
}

func (daemon *Daemon) attach(streamConfig *StreamConfig, openStdin, stdinOnce, tty bool, stdin io.ReadCloser, stdout io.Writer, stderr io.Writer) chan error {

	var (
		cStdout, cStderr       io.ReadCloser
		cStdin                 io.WriteCloser
		results                = make(chan attachResult, 3)
		stdoutDone, stderrDone bool
	)

	// Connect stdin of container to the http conn.
	if stdin != nil && openStdin {
		// Get the stdin pipe.
		if cStdin, err := streamConfig.StdinPipe(); err != nil {
			results <- attachResult{err: err, stream: streamStdin}
		} else {
			go func() {
				log.Debugf("attach: stdin: begin")
				defer log.Debugf("attach: stdin: end")
				if stdinOnce && !tty {
					defer cStdin.Close()
				} else {
					// No matter what, when stdin is closed (io.Copy unblock), close stdout and stderr
					defer func() {
						if cStdout != nil {
							cStdout.Close()
						}
						if cStderr != nil {
							cStderr.Close()
						}
					}()
				}
				if tty {
					_, err = utils.CopyEscapable(cStdin, stdin)
				} else {
					_, err = io.Copy(cStdin, stdin)

				}
				if err == io.ErrClosedPipe {
					err = nil
				}
				if err != nil {
					log.Errorf("attach: stdin: %s", err)
				}
				results <- attachResult{err: err, stream: streamStdin}
			}()
		}
	}
	if stdout != nil {
		// Get a reader end of a pipe that is attached as stdout to the container.
		if p, err := streamConfig.StdoutPipe(); err != nil {
			results <- attachResult{err: err, stream: streamStdout}
		} else {
			cStdout = p
			go func() {
				log.Debugf("attach: stdout: begin")
				defer log.Debugf("attach: stdout: end")
				_, err := io.Copy(stdout, cStdout)
				if err == io.ErrClosedPipe {
					err = nil
				}
				if err != nil {
					log.Errorf("attach: stdout: %s", err)
				}
				results <- attachResult{err: err, stream: streamStdout}
			}()
		}
	} else {
		// Point stdout of container to a no-op writer.
		go func() {
			if cStdout, err := streamConfig.StdoutPipe(); err != nil {
				log.Errorf("attach: stdout pipe: %s", err)
			} else {
				io.Copy(&ioutils.NopWriter{}, cStdout)
			}
			results <- attachResult{err: nil, stream: streamStdout}
		}()
	}
	if stderr != nil {
		if p, err := streamConfig.StderrPipe(); err != nil {
			results <- attachResult{err: err, stream: streamStderr}
		} else {
			cStderr = p
			go func() {
				log.Debugf("attach: stderr: begin")
				defer log.Debugf("attach: stderr: end")
				_, err := io.Copy(stderr, cStderr)
				if err == io.ErrClosedPipe {
					err = nil
				}
				if err != nil {
					log.Errorf("attach: stderr: %s", err)
				}
				results <- attachResult{err: err, stream: streamStderr}
			}()
		}
	} else {
		// Point stderr at a no-op writer.
		go func() {
			if cStderr, err := streamConfig.StderrPipe(); err != nil {
				log.Errorf("attach: stdout pipe: %s", err)
			} else {
				io.Copy(&ioutils.NopWriter{}, cStderr)
			}
			results <- attachResult{err: nil, stream: streamStderr}
		}()
	}

	return promise.Go(func() error {
		defer func() {
			if cStdout != nil {
				cStdout.Close()
			}
			if cStderr != nil {
				cStderr.Close()
			}
			if cStdin != nil {
				cStdin.Close()
			}
		}()

		for result := range results {
			if result.err != nil {
				log.Errorf("attach: %s job returned error %s, aborting all jobs", result.stream, result.err)
				return result.err
			}
			log.Debugf("attach: %s job completed successfully", result.stream)
			if result.stream == streamStdout {
				stdoutDone = true
			}
			if result.stream == streamStderr {
				stderrDone = true
			}
			if stdoutDone && stderrDone {
				break
			}
		}
		log.Debugf("attach: all jobs completed successfully")
		return nil
	})
}
