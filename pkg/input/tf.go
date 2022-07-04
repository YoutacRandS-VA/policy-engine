// Copyright 2022 Snyk Ltd
// Copyright 2021 Fugue, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package input

import (
	"fmt"
	"path/filepath"

	"github.com/sirupsen/logrus"

	"github.com/snyk/policy-engine/pkg/hcl_interpreter"
	"github.com/snyk/policy-engine/pkg/models"
)

// This is the loader that supports reading files and directories of HCL (.tf)
// files.  The implementation is in the `./pkg/hcl_interpreter/` package in this
// repository: this file just wraps that.  That directory also contains a
// README explaining how everything fits together.
type TfDetector struct{}

func (t *TfDetector) DetectFile(i *File, opts DetectOptions) (IACConfiguration, error) {
	if !opts.IgnoreExt && i.Ext() != ".tf" {
		return nil, fmt.Errorf("%w: %v", UnrecognizedFileExtension, i.Ext())
	}
	dir := filepath.Dir(i.Path)
	moduleTree, err := hcl_interpreter.ParseFiles(nil, i.Fs, false, dir, []string{i.Path}, opts.VarFiles)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", FailedToParseInput, err)
	}

	return newHclConfiguration(moduleTree)
}

func (t *TfDetector) DetectDirectory(i *Directory, opts DetectOptions) (IACConfiguration, error) {
	// First check that a `.tf` file exists in the directory.
	tfExists := false
	children, err := i.Children()
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		if c, ok := child.(*File); ok && c.Ext() == ".tf" {
			tfExists = true
		}
	}
	if !tfExists {
		return nil, nil
	}

	moduleRegister := hcl_interpreter.NewTerraformRegister(i.Path)
	moduleTree, err := hcl_interpreter.ParseDirectory(moduleRegister, nil, i.Path, opts.VarFiles)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", FailedToParseInput, err)
	}

	if moduleTree != nil {
		for _, warning := range moduleTree.Warnings() {
			logrus.Warn(warning)
		}
	}

	return newHclConfiguration(moduleTree)
}

type HclConfiguration struct {
	moduleTree *hcl_interpreter.ModuleTree
	evaluation *hcl_interpreter.Evaluation
}

func newHclConfiguration(moduleTree *hcl_interpreter.ModuleTree) (*HclConfiguration, error) {
	analysis := hcl_interpreter.AnalyzeModuleTree(moduleTree)
	evaluation, err := hcl_interpreter.EvaluateAnalysis(analysis)
	if err != nil {
		return nil, err
	}

	return &HclConfiguration{
		moduleTree: moduleTree,
		evaluation: evaluation,
	}, nil
}

func (c *HclConfiguration) LoadedFiles() []string {
	return c.moduleTree.LoadedFiles()
}

func (c *HclConfiguration) Location(path []interface{}) (LocationStack, error) {
	// Format is {resourceType, resourceId, attributePath...}
	if len(path) < 2 {
		return nil, nil
	}

	resourceId, ok := path[1].(string)
	if !ok {
		return nil, fmt.Errorf("Expected string resource ID in path")
	}

	ranges := c.evaluation.Location(resourceId, path[2:])
	locs := LocationStack{}
	for _, r := range ranges {
		locs = append(locs, Location{
			Path: r.Filename,
			Line: r.Start.Line,
			Col:  r.Start.Column,
		})
	}
	return locs, nil
}

func (c *HclConfiguration) ToState() models.State {
	resources := c.evaluation.Resources()

	namespace := c.moduleTree.FilePath()
	for k, resource := range resources {
		resource.Namespace = namespace
		resources[k] = resource
	}

	return models.State{
		InputType:           TerraformHCL.Name,
		EnvironmentProvider: "iac",
		Meta: map[string]interface{}{
			"filepath": c.moduleTree.FilePath(),
		},
		Resources: groupResourcesByType(resources),
		Scope: map[string]interface{}{
			"filepath": c.moduleTree.FilePath(),
		},
	}
}