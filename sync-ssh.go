package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"strings"
)

var verbose = flag.Bool("v", false, "verbose output")
var logFile = flag.String("l", "", "log file (default is stdout)")

func main() {
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	if *logFile != "" {
		out, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
		if err != nil {
			panic(err)
		}
		log.SetOutput(out)
	}
	if len(flag.Args()) != 2 {
		panic("usage: sync-space path/to/local/dir [user@]host:path/to/remote/dir")
	}
	localArg := flag.Args()[0]
	remoteArg := flag.Args()[1]

	log.Printf("Syncing '%s' to '%s'", localArg, remoteArg)
	if strings.Count(remoteArg, ":") != 1 {
		panic("remote path format: [user@]host:path (the colon must always be present)")
	}
	remoteParts := strings.Split(remoteArg, ":")
	remoteUserHost := remoteParts[0]
	var remoteUser string
	var remoteHost string
	switch strings.Count(remoteUserHost, "@") {
	case 0:
		// Use local user
		usr, err := user.Current()
		if err != nil {
			panic(err)
		}
		remoteUser = usr.Username
		remoteHost = remoteUserHost
		remoteUserHost = remoteUser + "@" + remoteHost
	case 1:
		// Provided
		ss := strings.Split(remoteUserHost, "@")
		remoteUser = ss[0]
		remoteHost = ss[1]
	default:
		panic(fmt.Sprintf("bad user@host string '%s'", remoteUserHost))
	}
	remotePath := remoteParts[1]
	if remotePath == "" {
		remotePath = "."
	}
	if remoteUserHost == "" {
		panic(fmt.Sprintf("bad remoteArg %s", remoteArg))
	}
	if *verbose {
		log.Printf("hostname %s", remoteUserHost)
	}
	user, err := user.Current()
	if err != nil {
		panic(err)
	}
	ctlDir := path.Join(user.HomeDir, ".ssh/ctl")
	os.MkdirAll(ctlDir, os.ModeDir)
	ctlPath := path.Join(ctlDir, "%L-%r@%h:%p")

	log.Print("Starting tunnel")
	tunCmdArgs := []string{
		"compute",
		"ssh",
		"--ssh-flag=-nNf",
		"--ssh-flag=-o ControlMaster=yes",
		"--ssh-flag=-o ControlPath=" + ctlPath,
		"--ssh-flag=-o StrictHostKeyChecking=no",
		"--ssh-flag=-o UserKnownHostsFile=/dev/null",
		remoteUserHost,
	}
	tunProc, err := startTerminalProcess(
		"gcloud",
		tunCmdArgs,
	)
	if err != nil {
		panic(err)
	}
	if state, err := tunProc.Wait(); err != nil || !state.Success() {
		panic(err)
	}
	defer func() {
		log.Print("Stopping tunnel")
		tunCmdProc, err := startTerminalProcess(
			"gcloud",
			[]string{
				"compute",
				"ssh",
				"--ssh-flag=-O exit",
				"--ssh-flag=-o ControlPath=" + ctlPath,
				remoteUserHost,
			},
		)
		if err != nil {
			log.Print(err)
		}
		if _, err := tunCmdProc.Wait(); err != nil {
			log.Print(err)
		}
	}()

	// The argument format of gcloud compute ssh differs from vanilla ssh enough
	// that gcloud compute ssh can't be used with the -e/--rsh flag of rsync.
	//
	// Workaround is to get the IP from gcloud and use ssh.

	gcloudArgs := []string{
		"compute",
		"instances",
		"describe",
		remoteHost,
		"--format=value(networkInterfaces.accessConfigs[0].natIP)",
	}
	gcloudCmd := exec.Command("gcloud", gcloudArgs...)
	out, err := gcloudCmd.CombinedOutput()
	if err != nil {
		panic(err)
	}
	remoteHostIP := strings.TrimSpace(string(out))
	if *verbose {
		log.Printf("host IP %s", remoteHostIP)
	}

	log.Printf("Starting initial sync")
	rsyncArgs := []string{
		"-rlptz",
		"--delete-during",
		"--exclude=.git/",
		"--exclude=bin/",
		"--exclude=pkg/",
		"--rsh=ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ControlPath=" + ctlPath,
		localArg,
		remoteUser + "@" + remoteHostIP + ":" + remotePath,
	}
	if *verbose {
		log.Printf("rsyncCmd: %v", exec.Command("rsync", rsyncArgs...))
	}
	irsyncProc, err := startTerminalProcess(
		"rsync",
		rsyncArgs,
	)
	if err != nil {
		panic(err)
	}
	if _, err := irsyncProc.Wait(); err != nil {
		panic(err)
	}
	log.Printf("Initial sync complete")

	events := make(chan string)
	log.Printf("Starting listener on %s", localArg)
	fswatchArgs := []string{
		"--one-per-batch",
		"--recursive",
		"--latency=3",
		"--exclude=.git/",
		"--exclude=bin/",
		"--exclude=pkg/",
		localArg,
	}
	fswatchCmd := exec.Command("fswatch", fswatchArgs...)
	watchPipe, err := fswatchCmd.StdoutPipe()
	if err != nil {
		panic(err)
	}
	if *verbose {
		log.Print(fswatchCmd)
	}
	if err := fswatchCmd.Start(); err != nil {
		panic(err)
	}
	defer func() {
		fswatchCmd.Process.Signal(os.Interrupt)
		if err := fswatchCmd.Wait(); err != nil {
			log.Print(err)
		}
	}()
	go func() {
		scanner := bufio.NewScanner(watchPipe)
		for scanner.Scan() {
			events <- scanner.Text()
		}
		if err := scanner.Err(); err != nil {
			log.Printf("fswatch scanner error: %v", err)
		}
		close(events)
	}()

	log.Printf("Starting syncer")
	rsyncTkt := make(chan struct{}, 1)
	rsyncTkt <- struct{}{}
	go func() {
		for {
			s, ok := <-events
			if !ok {
				log.Print("events closed")
				return
			}
			if *verbose {
				log.Printf("syncing: '%s'", s)
			}
			rsyncCmd := exec.Command("rsync", rsyncArgs...)
			buf := new(bytes.Buffer)
			rsyncCmd.Stdout = buf
			rsyncCmd.Stderr = buf
			<-rsyncTkt
			if err := rsyncCmd.Run(); err != nil {
				log.Print("rsync output:")
				log.Print(buf.String())
				log.Print(err)
				log.Print("syncer terminating")
				return
			}
			rsyncTkt <- struct{}{}
			if *verbose {
				log.Print("...sync ok")
			}
		}
	}()
	defer func() {
		// wait for rsync to finish, prevent another from starting
		<-rsyncTkt
	}()

	// Stdin gets handled weird when using exec, so use low-level processes and
	// pass fds directly.

	log.Printf("Starting shell")
	gcloudPath, err := exec.LookPath("gcloud")
	if err != nil {
		panic(err)
	}
	shellProc, err := startTerminalProcess(
		gcloudPath,
		[]string{
			"compute",
			"ssh",
			"--ssh-flag=-o ControlPath=" + ctlPath,
			remoteUserHost,
		},
	)
	if err != nil {
		panic(err)
	}
	if _, err := shellProc.Wait(); err != nil {
		panic(err)
	}
	log.Print("Shell exited")
}

// startTerminalProcess starts a process and wires it up to the current process'
// stdin, stdout and stderr by passing low-level fds.
func startTerminalProcess(path string, argv []string) (*os.Process, error) {
	if filepath.Base(path) == path {
		var err error
		path, err = exec.LookPath(path)
		if err != nil {
			return nil, err
		}
	}
	argv = append([]string{path}, argv...)
	if *verbose {
		log.Printf("Process: %v", argv)
	}
	proc, err := os.StartProcess(
		path,
		argv,
		&os.ProcAttr{
			Files: []*os.File{
				os.Stdin,
				os.Stdout,
				os.Stderr,
			},
		},
	)
	return proc, err
}
