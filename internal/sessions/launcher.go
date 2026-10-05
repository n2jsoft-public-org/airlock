package sessions

import (
	"bufio"
	"errors"
	"io"
	"os/exec"
	"sync"

	"github.com/n2jsoft/airlock/internal/platform"
)

// Process is a started aws process.
type Process interface {
	PID() int
	// Wait returns once the process exited and its output was read.
	Wait() error
	// Terminate asks the process and its children to stop.
	Terminate()
}

// Launcher starts the aws process of a session with args, sending each output line to output.
type Launcher func(args []string, output func(line string)) (Process, error)

// AWSLauncher starts aws with args in a process group of its own, so session-manager-plugin stops
// with it.
func AWSLauncher(args []string, output func(line string)) (Process, error) {
	// exec.Command, not CommandContext: the process group is stopped by Terminate.
	cmd := exec.Command("aws", args...) // #nosec G204 -- arguments are built by the jumphost runner.
	platform.StartInOwnGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &awsProcess{cmd: cmd}
	p.readers.Add(2)
	go p.read(stdout, output)
	go p.read(stderr, output)
	return p, nil
}

type awsProcess struct {
	cmd     *exec.Cmd
	readers sync.WaitGroup
}

func (p *awsProcess) PID() int { return p.cmd.Process.Pid }

func (p *awsProcess) Wait() error {
	p.readers.Wait()
	return p.cmd.Wait()
}

func (p *awsProcess) Terminate() { platform.TerminateGroup(p.cmd) }

func (p *awsProcess) read(reader io.Reader, output func(string)) {
	defer p.readers.Done()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		output(scanner.Text())
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		output("stream error: " + err.Error())
	}
}
