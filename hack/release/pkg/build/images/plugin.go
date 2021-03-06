/*
Copyright 2019 The Jetstack cert-manager contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package images

import (
	"context"
	"fmt"
	"github.com/jetstack/cert-manager/hack/release/pkg/util"
	flag "github.com/spf13/pflag"
	"os/exec"

	"github.com/jetstack/cert-manager/hack/release/pkg/bazel"
	"github.com/jetstack/cert-manager/hack/release/pkg/flags"
	logf "github.com/jetstack/cert-manager/hack/release/pkg/log"
)

var (
	Default = &Plugin{}

	supportedGoArch     = []string{"amd64", "arm64", "arm"}
	supportedComponents = []string{"acmesolver", "controller", "webhook", "cainjector"}
	log                 = logf.Log.WithName("images")
)

type Plugin struct {
	// The list of images to build (e.g. acmesolver, controller, webhook)
	Components []string

	// If true, the built images will be exported to the configured docker
	// daemon when the Build() method is called.
	ExportToDocker bool

	// List of architectures to build images for
	GoArch []string

	// TODO: add GOOS support once the build system supports more than linux

	// built is set to true if Build() has completed successfully
	built bool
}

func (g *Plugin) AddFlags(fs *flag.FlagSet) {
	fs.BoolVar(&g.ExportToDocker, "images.export", false, "if true, images will be exported to the currently configured docker daemon")
	fs.StringSliceVar(&g.Components, "images.components", []string{"acmesolver", "controller", "webhook"}, "the list of components to build images for")
	fs.StringSliceVar(&g.GoArch, "images.goarch", []string{"amd64", "arm64", "arm"}, "list of architectures to build images for")
}

func (g *Plugin) Validate() []error {
	var errs []error

	// validate goarch flag
	for _, a := range g.GoArch {
		valid := false
		for _, sa := range supportedGoArch {
			if a == sa {
				valid = true
				break
			}
		}
		if !valid {
			errs = append(errs, fmt.Errorf("invalid goarch value %q", a))
		}
	}

	// validate components flag
	for _, a := range g.Components {
		valid := false
		for _, sa := range supportedComponents {
			if a == sa {
				valid = true
				break
			}
		}
		if !valid {
			errs = append(errs, fmt.Errorf("invalid component name %q", a))
		}
	}

	return errs
}

func (g *Plugin) InitPublish() []error {
	var errs []error

	return errs
}

func (g *Plugin) Build(ctx context.Context) error {
	_, err := g.build(ctx)
	if err != nil {
		return err
	}

	if g.ExportToDocker {
		log.Info("Exporting docker images to local docker daemon")
		if err := g.exportToDocker(ctx); err != nil {
			return err
		}
	} else {
		log.Info("skipping exporting docker images to docker daemon")
	}

	return nil
}

func (g *Plugin) Publish(ctx context.Context) error {
	// this case should never be reached, but we check it to be safe
	if !g.built {
		if _, err := g.build(ctx); err != nil {
			return err
		}
	}

	targets := g.generateTargets()
	err := g.pushImages(ctx, targets)
	if err != nil {
		return err
	}

	log.Info("published all docker images")

	return nil
}

func (g *Plugin) Complete() error {
	return nil
}

func (g *Plugin) build(ctx context.Context) (imageTargets, error) {
	targets := g.generateTargets()

	bazelTargets := targets.bazelTargets()
	log := log.WithValues("images", bazelTargets)
	log.Info("building bazel image targets")

	// set the os and arch to linux/amd64
	// whilst we might be building cross-arch binaries, cgo only depends on
	// particular OS settings and not arch, so just by setting this to 'linux'
	// we can fix cross builds on platforms other than linux
	// if we support alternate OS values in future, this will need updating
	// with a call to BuildPlatformE per *OS*.
	err := bazel.Default.BuildPlatformE(ctx, log, "linux", "amd64", bazelTargets...)
	if err != nil {
		return nil, fmt.Errorf("error building docker images (%v): %v", targets, err)
	}

	g.built = true
	return targets, nil
}

func (g *Plugin) exportToDocker(ctx context.Context) error {
	targets := g.generateTargets()
	log.WithValues("images", targets.bazelExportTargets()).Info("exporting images to docker daemon")
	for _, target := range targets.bazelExportTargets() {
		log := log.WithValues("target", target)
		log.Info("exporting image to docker daemon")
		// set the os and arch to linux/amd64
		// whilst we might be building cross-arch binaries, cgo only depends on
		// particular OS settings and not arch, so just by setting this to 'linux'
		// we can fix cross builds on platforms other than linux
		// if we support alternate OS values in future, this will need updating
		// with a call to BuildPlatformE per *OS*.
		err := bazel.Default.RunPlatformE(ctx, log, "linux", "amd64", target)
		if err != nil {
			return fmt.Errorf("error exporting image %q to docker daemon: %v", target, err)
		}
	}

	log.WithValues("images", targets.exportedImageNames()).Info("exported all docker images")

	return nil
}

// generateTargets generates a list of Bazel target names that must be
// built for this invocation of the image builder
func (g *Plugin) generateTargets() imageTargets {
	var targets []imageTarget
	for _, c := range g.Components {
		for _, a := range g.GoArch {
			targets = append(targets, imageTarget{c, "linux", a})
		}
	}
	return targets
}

// pushImages will push the images built for this release to the registry
// TODO: add support for calling container_push targets instead of just 'docker push'
func (p *Plugin) pushImages(ctx context.Context, targets imageTargets) error {
	err := p.exportToDocker(ctx)
	if err != nil {
		return err
	}

	var images []string
	for _, t := range targets {
		images = append(images, t.exportedImageNames()...)
	}

	log.WithValues("images", images).Info("pushing docker images")
	for _, img := range images {
		log := log.WithValues("image", img)
		log.Info("pushing docker image")
		err := util.RunE(log, exec.CommandContext(ctx, "docker", "push", img))
		if err != nil {
			return err
		}
	}

	return nil
}

type imageTargets []imageTarget

func (i imageTargets) bazelTargets() []string {
	out := make([]string, len(i))
	for idx, target := range i {
		out[idx] = target.bazelTarget()
	}
	return out
}

func (i imageTargets) bazelExportTargets() []string {
	out := make([]string, len(i))
	for idx, target := range i {
		out[idx] = target.bazelExportTarget()
	}
	return out
}

func (i imageTargets) exportedImageNames() []string {
	out := make([]string, 0)
	for _, target := range i {
		out = append(out, target.exportedImageNames()...)
	}
	return out
}

type imageTarget struct {
	name, os, arch string
}

func (i imageTarget) bazelTarget() string {
	return fmt.Sprintf("//cmd/%s:image.%s-%s", i.name, i.os, i.arch)
}

func (i imageTarget) bazelExportTarget() string {
	return fmt.Sprintf("//cmd/%s:image.%s-%s.export", i.name, i.os, i.arch)
}

func (i imageTarget) exportedImageNames() []string {
	if i.arch == "amd64" {
		return []string{
			fmt.Sprintf("%s/cert-manager-%s:%s", flags.Default.DockerRepo, i.name, flags.Default.AppVersion),
			fmt.Sprintf("%s/cert-manager-%s:%s", flags.Default.DockerRepo, i.name, flags.Default.GitCommitRef),
		}
	}

	return []string{
		fmt.Sprintf("%s/cert-manager-%s-%s:%s", flags.Default.DockerRepo, i.name, i.arch, flags.Default.AppVersion),
		fmt.Sprintf("%s/cert-manager-%s-%s:%s", flags.Default.DockerRepo, i.name, i.arch, flags.Default.GitCommitRef),
	}
}
