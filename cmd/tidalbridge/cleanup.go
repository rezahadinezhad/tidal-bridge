package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// Agents start dev servers and watchers in the background and often never
// stop them; on a 16 GB laptop they are among the largest users of memory.
// cleanup lists such process trees and stops the ones named (or every
// forgotten one) only when asked. Nothing else is ever touched.
var devProcess = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`next(\.js)?["']?\s+dev\b`, `\bvite(\.js)?["']?(\s+(dev|serve)\b|\s+--|\s*$)`, `\bvitest(\.mjs)?["']?(\s+watch\b|\s*$|\s+--watch)`,
	`\bjest(\.js)?["']?.*--watch`, `\btsc["']?.*\s(-w|--watch)\b`, `\bnodemon\b`, `webpack(-cli)?["']?.*\sserve\b`,
	`manage\.py["']?\s+runserver\b`, `\buvicorn\b.*--reload`, `\bcelery\b.*\bworker\b`, `\bdaphne\b`, `\bnpm(-cli\.js)?["']?\s+run\s+dev\b`,
}, "|"))

var devRuntimes = map[string]bool{"node.exe": true, "python.exe": true, "node": true, "python": true, "python3": true}

type devTree struct {
	PID       int32
	Started   time.Time
	MemoryMB  float64
	Forgotten bool
	Command   string
}

type procInfo struct {
	parent  int32
	created int64
	rss     float64
	command string
}

// findDevTrees returns the outermost process of every dev-tool tree with the
// memory of the whole tree. A tree is forgotten when the process that
// started it (a terminal or an agent) has exited.
func findDevTrees() ([]devTree, error) {
	all, err := process.Processes()
	if err != nil {
		return nil, err
	}
	procs := map[int32]*procInfo{}
	children := map[int32][]int32{}
	var candidates []int32
	for _, p := range all {
		i := &procInfo{}
		i.parent, _ = p.Ppid()
		i.created, _ = p.CreateTime()
		if m, e := p.MemoryInfo(); e == nil {
			i.rss = float64(m.RSS) / (1 << 20)
		}
		procs[p.Pid] = i
		children[i.parent] = append(children[i.parent], p.Pid)
		if name, e := p.Name(); e == nil && devRuntimes[strings.ToLower(name)] {
			if command, e := p.Cmdline(); e == nil && devProcess.MatchString(command) && !strings.Contains(strings.ToLower(command), "tidalbridge") {
				i.command = command
				candidates = append(candidates, p.Pid)
			}
		}
	}
	var trees []devTree
	for _, pid := range candidates {
		i := procs[pid]
		parent, alive := procs[i.parent]
		alive = alive && parent.created <= i.created
		nested := false
		for id, depth := pid, 0; depth < 16 && !nested; depth++ {
			next, ok := procs[procs[id].parent]
			if !ok || next.created > procs[id].created || procs[id].parent == id {
				break
			}
			id = procs[id].parent
			nested = next.command != "" // reported from its outer dev process
		}
		if nested {
			continue
		}
		var memory float64
		var walk func(int32)
		walk = func(id int32) {
			memory += procs[id].rss
			for _, child := range children[id] {
				if procs[child].created >= procs[id].created {
					walk(child)
				}
			}
		}
		walk(pid)
		trees = append(trees, devTree{PID: pid, Started: time.UnixMilli(i.created), MemoryMB: memory, Forgotten: !alive, Command: i.command})
	}
	sort.Slice(trees, func(a, b int) bool { return trees[a].MemoryMB > trees[b].MemoryMB })
	return trees, nil
}

func cleanupCommand(args []string) (int, error) {
	stop := map[int32]bool{}
	stopForgotten := false
	for _, arg := range args {
		if arg == "--stop-forgotten" {
			stopForgotten = true
		} else if pid, e := strconv.Atoi(arg); e == nil {
			stop[int32(pid)] = true
		} else {
			return 1, fmt.Errorf("usage: cleanup [--stop-forgotten] [PID...]")
		}
	}
	trees, err := findDevTrees()
	if err != nil {
		return 1, err
	}
	if len(trees) == 0 {
		fmt.Println("No dev servers or watchers are running.")
		return 0, nil
	}
	total := 0.0
	for _, t := range trees {
		state := "running"
		if t.Forgotten {
			state = "forgotten"
		}
		command := t.Command
		if len(command) > 90 {
			command = command[:87] + "..."
		}
		fmt.Printf("%7d  %6.0f MB  %-9s  since %s  %s\n", t.PID, t.MemoryMB, state, t.Started.Format("Jan 2 15:04"), command)
		total += t.MemoryMB
		if stop[t.PID] || stopForgotten && t.Forgotten {
			if e := stopTree(t.PID); e != nil {
				fmt.Fprintf(os.Stderr, "  could not stop %d: %v\n", t.PID, e)
			} else {
				fmt.Printf("  stopped %d\n", t.PID)
			}
		}
	}
	fmt.Printf("%d dev process tree(s), %.0f MB in total. Stop one with `tidalbridge cleanup PID`, or every forgotten one with --stop-forgotten.\n", len(trees), total)
	return 0, nil
}

// stopTree ends a process and its descendants, children first.
func stopTree(pid int32) error {
	p, err := process.NewProcess(pid)
	if err != nil {
		return err
	}
	children, _ := p.Children()
	for _, child := range children {
		stopTree(child.Pid)
	}
	return p.Kill()
}
