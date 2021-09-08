package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v2"
	"k8s.io/klog/v2"
)

var targetFlag = flag.String("target", "minikube", "What kind of cluster to target: kind, minikube, k3d, remote")
var sceneFlag = flag.String("scene", "", "configuration file to load test cases from")
var timeoutFlag = flag.Duration("timeout", 6*time.Minute, "maximum time a command can take")

// Requirements describes the requirements of the target cluster
type Requirements struct {
	KubernetesVersion string `yaml:"kubernetes-version"`
	ControlPlanes     int
	Workers           int
	CNI               string
}

type Xfer struct {
	Source string
	Dest   string
	Target string
}

type Step struct {
	Local        string
	Transfer     Xfer
	ControlPlane string `yaml:"control-plane"`
	Worker       string
	Background   bool
}

type Scenario struct {
	Requirements Requirements
	Setup        []Step
}

// RunResult stores the result of an cmd.Run call
type RunResult struct {
	Stdout   *bytes.Buffer
	Stderr   *bytes.Buffer
	ExitCode int
	Duration time.Duration
	Args     []string
}

// Run is a helper to log command execution
func Run(cmd *exec.Cmd) (*RunResult, error) {
	rr := &RunResult{Args: cmd.Args}

	var outb, errb bytes.Buffer
	cmd.Stdout, rr.Stdout = &outb, &outb
	cmd.Stderr, rr.Stderr = &errb, &errb

	start := time.Now()
	klog.V(1).Infof("Running: %s", cmd)
	err := cmd.Run()
	rr.Duration = time.Since(start)

	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			rr.ExitCode = exitError.ExitCode()
		}
		klog.Errorf("cmd.Run returned error: %v", err)
	}

	klog.V(1).Infof("Completed: %s (duration: %s, exit code: %d, err: %v)", cmd, rr.Duration, rr.ExitCode, err)
	if len(rr.Stderr.Bytes()) > 0 {
		klog.Warningf("%s", rr.Stderr.String())
	}

	if err == nil {
		return rr, err
	}
	return rr, fmt.Errorf("%s: %w, stderr=%s", cmd.Args, err, errb.String())
}

func runStep(s Step, d time.Duration) error {
	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()

	cmd := ""
	args := []string{}

	klog.V(1).Infof("STEP: %+v", s)
	switch {
	case s.Local != "":
		cmd = "sh"
		args = []string{"-c", s.Local}
	case s.ControlPlane != "":
		// TODO: Add support for other environments
		cmd = "minikube"
		args = []string{"ssh", s.ControlPlane}
	case s.Transfer.Source != "":
		t := s.Transfer
		cmd = "minikube"
		// TODO: Add support for non-cp transfers
		target := "minikube"
		args = []string{"cp", t.Source, fmt.Sprintf("%s:%s", target, t.Dest)}
	}

	_, err := Run(exec.CommandContext(ctx, cmd, args...))
	if err != nil {
		return err
	}
	return nil
}

func ensureRequirements(r Requirements, d time.Duration) error {
	klog.Infof("Ensuring requirements are met: %+v", r)
	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()

	// TODO: Add support for other environments
	args := []string{"minikube", "start", "--kubernetes-version", r.KubernetesVersion}
	klog.Infof("Setting up cluster: %v", args)
	_, err := Run(exec.CommandContext(ctx, args[0], args[1:]...))
	return err
}

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	if *sceneFlag == "" {
		klog.Exitf("--scene is a required flag. Try scenarios/005, for example")
	}

	f, err := ioutil.ReadFile(filepath.Join(*sceneFlag, "scene.yaml"))
	if err != nil {
		klog.Exitf("readfile failed: %v", err)
	}

	if err := os.Chdir(filepath.Dir(*sceneFlag)); err != nil {
		klog.Exitf("chdir failed: %v", err)
	}

	s := &Scenario{}
	err = yaml.Unmarshal(f, &s)
	if err != nil {
		klog.Exitf("unmarshal: %w", err)
	}

	if err := ensureRequirements(s.Requirements, *timeoutFlag); err != nil {
		klog.Errorf("unable to meet requirements: %v", err)
		os.Exit(1)
	}

	backgrounded := 0
	for i, step := range s.Setup {
		klog.Infof("Running step %d of %d ...", i, len(s.Setup))
		if step.Background {
			go func() {
				klog.V(1).Infof("Running in background: %+v", step)
				err := runStep(step, *timeoutFlag)
				if err != nil {
					klog.Errorf("background step %d failed: %v", i, err)
				}
			}()
			backgrounded++
			continue
		}

		err := runStep(step, *timeoutFlag)
		if err != nil {
			klog.Errorf("step %d failed: %v", i, err)
			os.Exit(2)
		}
	}

	if backgrounded > 0 {
		klog.Infof("Scenario is live! Hit Ctrl-C to abort.")
		for {
			time.Sleep(1 * time.Second)
		}
	}
}
