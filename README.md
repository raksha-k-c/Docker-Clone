# mydocker

A minimal container runtime built from scratch in Go, to understand what `docker run` actually does under the hood. No Docker, no external container libraries — just raw Linux syscalls (namespaces, cgroups, chroot) wired together by hand.

## What this does

Running `mydocker run sh` gives you a shell that is:
- Isolated by hostname (its own UTS namespace)
- Isolated by process ID (its own PID namespace — the shell thinks it's PID 1)
- Isolated by filesystem (chrooted into its own root filesystem, with a fresh `/proc`)
- Resource-limited (cgroups v2 caps on memory and process count)
- Network-isolated but internet-connected (its own network namespace, wired to the host via a veth pair and NAT)

Every one of these is a real Linux kernel feature — the same primitives `runc` and Docker's engine are built on.

## Architecture

mydocker run <command>
|
|-- re-execs itself via /proc/self/exe as "child"
| with CLONE_NEWUTS | CLONE_NEWPID | CLONE_NEWNS | CLONE_NEWNET
|
+-- child process:
|-- joins a cgroup (memory + pids limits)
|-- sets hostname
|-- chroots into ./rootfs (Alpine Linux minimal filesystem)
|-- mounts a fresh /proc
+-- execs the requested command

Networking (veth pair, IP addressing, NAT) is set up separately from the host side, since it requires reaching into the container's namespace after it starts — see "Networking setup" below.

## Requirements

- Linux (or WSL2 on Windows)
- Go 1.20+
- Root/sudo access (namespaces require CAP_SYS_ADMIN)
- iptables (for the networking stage)

## Setup

```bash
git clone https://github.com/raksha-k-c/Docker-Clone.git
cd Docker-Clone

# Download a minimal root filesystem to chroot into
mkdir -p rootfs
curl -L -o alpine.tar.gz https://dl-cdn.alpinelinux.org/alpine/v3.20/releases/x86_64/alpine-minirootfs-3.20.3-x86_64.tar.gz
tar -xzf alpine.tar.gz -C rootfs
```

## Usage

```bash
sudo go run main.go run sh
```

This drops you into an isolated shell. Try these inside it:

```sh
hostname          # prints "mycontainer" - isolated from the host
ps aux            # shows only processes inside this container
ip addr           # empty network namespace (just loopback, until networking is set up)
```

From the HOST (not inside the container), you can check the enforced resource limits:
```bash
cat /sys/fs/cgroup/mydocker/memory.max
```

## Networking setup (optional, gives the container internet access)

Namespaces alone leave the container's network completely empty. To give it connectivity:

**1. On the host, one-time setup:**
```bash
sudo sysctl -w net.ipv4.ip_forward=1
sudo iptables -t nat -A POSTROUTING -s 10.0.0.0/24 -o eth0 -j MASQUERADE
sudo iptables -A FORWARD -i veth0 -o eth0 -j ACCEPT
sudo iptables -A FORWARD -i eth0 -o veth0 -j ACCEPT
```
(Replace eth0 with your host's actual internet-facing interface - check with `ip route | grep default`.)

**2. With the container running, find its PID from another terminal:**
```bash
ps aux | grep "child sh"
```

**3. Create a veth pair and attach one end to the container's namespace:**
```bash
sudo ip link add veth0 type veth peer name veth1
sudo ip link set veth1 netns <container-pid>
sudo ip addr add 10.0.0.1/24 dev veth0
sudo ip link set veth0 up
```

**4. Inside the container:**
```sh
ip addr add 10.0.0.2/24 dev veth1
ip link set veth1 up
ip route add default via 10.0.0.1
ping 8.8.8.8
```

## What each stage taught me

| Stage | Kernel feature | What it proves |
|---|---|---|
| 1 | CLONE_NEWUTS | Changing hostname inside the namespace doesn't affect the host |
| 2 | CLONE_NEWPID | The process believes it's PID 1, fully unaware of host processes |
| 3 | CLONE_NEWNS + chroot + fresh /proc | ps aux shows only container processes - filesystem view is fully isolated |
| 4 | cgroups v2 (memory.max, pids.max) | Verified by intentionally exceeding the pids limit - the kernel refused new forks |
| 5 | CLONE_NEWNET + veth pair + NAT | Container starts with zero network access, then gets real internet connectivity through a hand-built virtual cable |

## Known limitations (compared to real Docker)

- Uses chroot instead of pivot_root (real container runtimes use pivot_root for better security isolation)
- No image layering / union filesystem - just a flat extracted rootfs
- Networking setup is manual/scripted, not automated inside main.go
- No image pull/push, registry support, or Dockerfile-equivalent build system
- This project uses `chroot` for filesystem isolation rather than `pivot_root`, which is what production container runtimes (including Docker's `runc`) use for stronger security. I implemented `pivot_root` (see the `pivotRoot()` function in main.go) and tested it, but it consistently failed with `EINVAL` in this development environment (WSL2), which has known quirks around mount namespace operations due to its virtualized filesystem layer. On a standard Linux install, the same `pivot_root` code should work as expected. I kept the implementation in the codebase (unused) to document the attempt and the reasoning.

## Why Go

Real container tooling (runc, containerd, Docker's engine, Kubernetes) is written in Go, and its standard library exposes namespace flags as a typed struct (syscall.SysProcAttr.Cloneflags) rather than requiring raw C-style syscalls. This made it possible to focus on the concepts rather than fighting language plumbing.