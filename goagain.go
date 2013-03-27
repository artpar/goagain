// Zero-downtime restarts in Go.
package goagain

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"reflect"
	"syscall"
)

// Export an error equivalent to net.errClosing for use with Accept during
// a graceful exit.
var ErrClosing = errors.New("use of closed network connection")

// Block this goroutine awaiting signals.  With the exception of SIGTERM
// taking the place of SIGQUIT, signals are handled exactly as in Nginx
// and Unicorn: <http://unicorn.bogomips.org/SIGNALS.html>.
func AwaitSignals(l *net.TCPListener) error {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGUSR2)
	for {
		sig := <-ch
		log.Println(sig.String())
		switch sig {

		// TODO SIGHUP should reload configuration.

		// SIGQUIT should exit gracefully.  However, Go doesn't seem
		// to like handling SIGQUIT (or any signal which dumps core by
		// default) at all so SIGTERM takes its place.  How graceful
		// this exit is depends on what the program does after this
		// function returns control.
		case syscall.SIGTERM:
			return nil

		// TODO SIGUSR1 should reopen logs.

		// SIGUSR2 begins the process of restarting without dropping
		// the listener passed to this function.
		case syscall.SIGUSR2:
			err := Relaunch(l)
			if nil != err {
				return err
			}

		}
	}
	return nil // It'll never get here.
}

// Convert and validate the GOAGAIN_FD, GOAGAIN_NAME, and GOAGAIN_PPID
// environment variables.  If all three are present and in order, this
// is a child process that may pick up where the parent left off.
func GetEnvs() (l *net.TCPListener, ppid int, err error) {
	var fd uintptr
	_, err = fmt.Sscan(os.Getenv("GOAGAIN_FD"), &fd)
	if nil != err {
		return
	}
	var i net.Listener
	i, err = net.FileListener(os.NewFile(fd, os.Getenv("GOAGAIN_NAME")))
	if nil != err {
		return
	}
	l = i.(*net.TCPListener)
	if err = syscall.Close(int(fd)); nil != err {
		return
	}
	_, err = fmt.Sscan(os.Getenv("GOAGAIN_PPID"), &ppid)
	if nil != err {
		return
	}
	if syscall.Getppid() != ppid {
		err = errors.New(fmt.Sprintf(
			"GOAGAIN_PPID is %d but parent is %d\n",
			ppid,
			syscall.Getppid(),
		))
		return
	}
	return
}

// Send SIGQUIT (but really SIGTERM since Go can't handle SIGQUIT) to the
// given ppid in order to complete the handoff to the child process.
func KillParent(ppid int) error {
	err := syscall.Kill(ppid, syscall.SIGTERM)
	if nil != err {
		return err
	}
	return nil
}

// Re-exec this image without dropping the listener passed to this function.
func Relaunch(l *net.TCPListener) error {
	argv0, err := exec.LookPath(os.Args[0])
	if nil != err {
		return err
	}
	wd, err := os.Getwd()
	if nil != err {
		return err
	}
	v := reflect.ValueOf(l).Elem().FieldByName("fd").Elem()
	fd := uintptr(v.FieldByName("sysfd").Int())
	if err := os.Setenv("GOAGAIN_FD", fmt.Sprint(fd)); nil != err {
		return err
	}
	if err := os.Setenv("GOAGAIN_NAME", fmt.Sprintf("tcp:%s->", l.Addr().String())); nil != err {
		return err
	}
	if err := os.Setenv("GOAGAIN_PPID", fmt.Sprint(syscall.Getpid())); nil != err {
		return err
	}
	files := make([]*os.File, fd+1)
	files[syscall.Stdin] = os.Stdin
	files[syscall.Stdout] = os.Stdout
	files[syscall.Stderr] = os.Stderr
	files[fd] = os.NewFile(fd, string(v.FieldByName("sysfile").String()))
	p, err := os.StartProcess(argv0, os.Args, &os.ProcAttr{
		Dir:   wd,
		Env:   os.Environ(),
		Files: files,
		Sys:   &syscall.SysProcAttr{},
	})
	if nil != err {
		return err
	}
	log.Printf("spawned child %d\n", p.Pid)
	return nil
}
