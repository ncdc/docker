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

		<-daemon.Attach(&container.StreamConfig, container.Config.OpenStdin, container.Config.StdinOnce, container.Config.Tty, cStdin, cStdout, cStderr)
		// If we are in stdinonce mode, wait for the process to end
		// otherwise, simply return
		if container.Config.StdinOnce && !container.Config.Tty {
			container.WaitStop(-1 * time.Second)
		}
	}
	return engine.StatusOK
}

// FIXME: this should be private, and every outside subsystem
// should go through the "container_attach" job. But that would require
// that job to be properly documented, as well as the relationship between
// Attach and ContainerAttach.
//
// This method is in use by builder/builder.go.
func (daemon *Daemon) Attach(streamConfig *StreamConfig, openStdin, stdinOnce, tty bool, stdin io.ReadCloser, stdout io.Writer, stderr io.Writer) chan error {
	var (
		cStdout, cStderr       io.ReadCloser
		cStdin                 io.WriteCloser
		errors                 = make(chan error, 3)
		stdoutDone, stderrDone bool
	)

	// Connect stdin of container to the http conn.
	if stdin != nil && openStdin {
		// Get the stdin pipe.
		if cStdin, err := streamConfig.StdinPipe(); err != nil {
			errors <- err
		} else {
			go func() {
				log.Debugf("attach: stdin: begin")
				defer log.Debugf("attach: stdin: end")
				defer cStdin.Close()
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
				errors <- err
			}()
		}
	}
	if stdout != nil {
		// Get a reader end of a pipe that is attached as stdout to the container.
		if p, err := streamConfig.StdoutPipe(); err != nil {
			stdoutDone = true
			errors <- err
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
				stdoutDone = true
				errors <- err
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
			stdoutDone = true
		}()
	}
	if stderr != nil {
		if p, err := streamConfig.StderrPipe(); err != nil {
			stderrDone = true
			errors <- err
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
				stderrDone = true
				errors <- err
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
			stderrDone = true
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

		// FIXME: how to clean up the stdin goroutine without the unwanted side effect
		// of closing the passed stdin? Add an intermediary io.Pipe?
		for err := range errors {
			if err != nil {
				log.Errorf("attach: got error %s, aborting all jobs", err)
				return err
			}
			if stdoutDone && stderrDone {
				break
			}
		}
		log.Debugf("attach: all jobs completed successfully")
		return nil
	})
}
