package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func main() {
	switch os.Args[1] {
	case "run":
		run()
	case "child":
		child()
	default:
		panic("bad command")
	}
}

func run() {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	memory := fs.String("memory", "100m", "memory limit (e.g. 100m, 512m, 1g)")
	pids := fs.Int("pids", 20, "max number of processes")
	fs.Parse(os.Args[2:])

	command := fs.Args()
	if len(command) == 0 {
		fmt.Println("Error: no command given. Usage: mydocker run [--memory 100m] [--pids 20] <command>")
		os.Exit(1)
	}

	fmt.Printf("Running %v as PID %d (memory=%s, pids=%d)\n", command, os.Getpid(), *memory, *pids)

	// Create a pipe: child will block reading from it until we write "ready"
	readPipe, writePipe, err := os.Pipe()
	must(err)

	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, command...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.ExtraFiles = []*os.File{readPipe} // becomes fd 3 inside the child

	cmd.Env = append(os.Environ(),
		"MYDOCKER_MEMORY="+*memory,
		"MYDOCKER_PIDS="+strconv.Itoa(*pids),
	)

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS | syscall.CLONE_NEWNET,
	}

	must(cmd.Start()) // Start (not Run!) so we can act while the child waits

	// Child is now alive, paused, waiting to read from the pipe.
	// Set up networking using the child's real PID.
	setupNetworking(cmd.Process.Pid)

	// Signal the child: networking is ready, proceed.
	writePipe.Write([]byte("ready"))
	writePipe.Close()

	// Now wait for the child to actually finish running the command.
	if err := cmd.Wait(); err != nil {
		fmt.Println("Error:", err)
	}

	cleanupNetworking()
}

func child() {
	// Wait for the parent to signal that networking is ready
	pipe := os.NewFile(3, "pipe")
	buf := make([]byte, 5)
	pipe.Read(buf)
	pipe.Close()

	fmt.Printf("Running %v as PID %d (inside new namespace)\n", os.Args[2:], os.Getpid())

	cg()

	must(syscall.Sethostname([]byte("mycontainer")))
	fmt.Printf("Running %v as PID %d (inside new namespace)\n", os.Args[2:], os.Getpid())

	cg()

	must(syscall.Sethostname([]byte("mycontainer")))

	runCmd("ip", "link", "set", "lo", "up")
	runCmd("ip", "addr", "add", "10.0.0.2/24", "dev", "veth1")
	runCmd("ip", "link", "set", "veth1", "up")
	runCmd("ip", "route", "add", "default", "via", "10.0.0.1")

	must(syscall.Chroot("rootfs"))
	must(syscall.Chroot("rootfs"))
	must(os.Chdir("/"))

	must(syscall.Mount("proc", "proc", "proc", 0, ""))

	cmd := exec.Command(os.Args[2], os.Args[3:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Println("Error:", err)
	}

	must(syscall.Unmount("proc", 0))
}

func cg() {
	cgroupPath := "/sys/fs/cgroup/mydocker"

	must(os.MkdirAll(cgroupPath, 0755))

	memory := os.Getenv("MYDOCKER_MEMORY")
	if memory == "" {
		memory = "100m"
	}
	memoryBytes, err := parseMemory(memory)
	must(err)

	must(os.WriteFile(cgroupPath+"/memory.max", []byte(strconv.FormatInt(memoryBytes, 10)), 0700))

	pids := os.Getenv("MYDOCKER_PIDS")
	if pids == "" {
		pids = "20"
	}
	must(os.WriteFile(cgroupPath+"/pids.max", []byte(pids), 0700))

	must(os.WriteFile(cgroupPath+"/cgroup.procs", []byte(strconv.Itoa(os.Getpid())), 0700))
}

func parseMemory(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	multiplier := int64(1)

	switch {
	case strings.HasSuffix(s, "g"):
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "g")
	case strings.HasSuffix(s, "m"):
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(s, "m")
	case strings.HasSuffix(s, "k"):
		multiplier = 1024
		s = strings.TrimSuffix(s, "k")
	}

	value, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory value %q: %w", s, err)
	}

	return value * multiplier, nil
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func runCmd(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("Error running %s %v: %v\n", name, args, err)
	}
}

func setupNetworking(pid int) {
	runCmd("ip", "link", "add", "veth0", "type", "veth", "peer", "name", "veth1")
	runCmd("ip", "link", "set", "veth1", "netns", strconv.Itoa(pid))
	runCmd("ip", "addr", "add", "10.0.0.1/24", "dev", "veth0")
	runCmd("ip", "link", "set", "veth0", "up")
	fmt.Println("Networking: veth pair created, host side configured (10.0.0.1)")
}

func cleanupNetworking() {
	runCmd("ip", "link", "delete", "veth0")
	fmt.Println("Networking: veth pair deleted")
}