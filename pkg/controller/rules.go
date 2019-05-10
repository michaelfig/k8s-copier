package controller

import (
	"errors"
	"strings"

	log "k8s.io/klog"
)

type Rule struct {
	Apply      func(*Controller, *Rule, *ResourceInstance) error
	Target     *Resource
	TargetPath string
	Source     Source
}

func ApplyReplaceRule(c *Controller, rule *Rule, target *ResourceInstance) error {
	data, err := rule.Source.Data()
	if err != nil {
		return err
	}
	log.Infof("FIXME: would replace %s on %s from %s", rule.TargetPath, target.Resource.Key(), data)
	return nil
}

func ParseRule(target, source, defaultNamespace string) (*Rule, error) {
	rule := &Rule{}
	if targetPath := strings.TrimPrefix(target, "replace-"); targetPath != target {
		rule.TargetPath = targetPath
		rule.Apply = ApplyReplaceRule
	} else {
		return nil, errors.New("Unrecognized target prefix " + target)
	}

	src, err := ParseSource(source, defaultNamespace)
	if err != nil {
		return nil, err
	}

	rule.Source = src
	return rule, nil
}
