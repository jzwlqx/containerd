package shim

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/crosbymichael/console"
	runc "github.com/crosbymichael/go-runc"
	apishim "github.com/docker/containerd/api/shim"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

type execProcess struct {
	sync.WaitGroup

	id      int
	console console.Console
	io      runc.IO
	status  int
	pid     int

	parent *initProcess
}

func newExecProcess(context context.Context, r *apishim.ExecRequest, parent *initProcess, id int) (process, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	e := &execProcess{
		id:     id,
		parent: parent,
	}
	var (
		socket  *runc.ConsoleSocket
		io      runc.IO
		pidfile = filepath.Join(cwd, fmt.Sprintf("%d.pid", id))
	)
	if r.Terminal {
		if socket, err = runc.NewConsoleSocket(filepath.Join(cwd, "pty.sock")); err != nil {
			return nil, err
		}
		defer os.Remove(socket.Path())
	} else {
		// TODO: get uid/gid
		if io, err = runc.NewPipeIO(0, 0); err != nil {
			return nil, err
		}
		e.io = io
	}
	opts := &runc.ExecOpts{
		PidFile:       pidfile,
		ConsoleSocket: socket,
		IO:            io,
		Detach:        true,
	}
	if err := parent.runc.Exec(context, parent.id, processFromRequest(r), opts); err != nil {
		return nil, err
	}
	if socket != nil {
		console, err := socket.ReceiveMaster()
		if err != nil {
			return nil, err
		}
		e.console = console
		if err := copyConsole(context, console, r.Stdin, r.Stdout, r.Stderr, &e.WaitGroup); err != nil {
			return nil, err
		}
	} else {
		if err := copyPipes(context, io, r.Stdin, r.Stdout, r.Stderr, &e.WaitGroup); err != nil {
			return nil, err
		}
	}
	pid, err := runc.ReadPidFile(opts.PidFile)
	if err != nil {
		return nil, err
	}
	e.pid = pid
	return e, nil
}

func processFromRequest(r *apishim.ExecRequest) specs.Process {
	var user specs.User
	if r.User != nil {
		user.UID = r.User.Uid
		user.GID = r.User.Gid
		user.AdditionalGids = r.User.AdditionalGids
	}
	return specs.Process{
		Terminal:        r.Terminal,
		User:            user,
		Rlimits:         rlimits(r.Rlimits),
		Args:            r.Args,
		Env:             r.Env,
		Cwd:             r.Cwd,
		Capabilities:    r.Capabilities,
		NoNewPrivileges: r.NoNewPrivileges,
		ApparmorProfile: r.ApparmorProfile,
		SelinuxLabel:    r.SelinuxLabel,
	}
}

func rlimits(rr []*apishim.Rlimit) (o []specs.LinuxRlimit) {
	for _, r := range rr {
		o = append(o, specs.LinuxRlimit{
			Type: r.Type,
			Hard: r.Hard,
			Soft: r.Soft,
		})
	}
	return o
}

func (e *execProcess) Pid() int {
	return e.pid
}

func (e *execProcess) Status() int {
	return e.status
}

func (e *execProcess) Exited(status int) {
	e.status = status
	e.Wait()
	if e.io != nil {
		e.io.Close()
	}
}

func (e *execProcess) Delete(ctx context.Context) error {
	return nil
}

func (e *execProcess) Resize(ws console.WinSize) error {
	if e.console == nil {
		return nil
	}
	return e.console.Resize(ws)
}
