package controller

import (
	"errors"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	log "k8s.io/klog"
)

type Rule struct {
	Apply      func(*Controller, *Rule, *ResourceInstance) error
	Target     *Resource
	TargetPath string
	Source     Source
}

func ApplyReplaceRule(c *Controller, rule *Rule, target *ResourceInstance) error {
	data, err := rule.Source.GetData()
	if err != nil {
		return err
	}

	log.Infof("Replacing %s:%s with %s", target.Resource.Key(), rule.TargetPath, data)
	if data == nil {
		log.Infof("Cannot replace %s:%s, no data from source",
			rule.Target.Key(), rule.TargetPath)
		return nil
	}

	res := c.dynclient.Resource(target.GVR)
	nsres := res.Namespace(target.Namespace)
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(target.Object)
	if err != nil {
		log.Infof("Cannot convert to unstructured: %s", err)
		return err
	}

	// Update the target object.
	path := strings.Split(rule.TargetPath, ".")
	err = unstructured.SetNestedField(obj, data, path...)
	if err != nil {
		log.Infof("Cannot set field %s: %s", rule.TargetPath, err)
		return err
	}

	// Run the update.
	unObj := &unstructured.Unstructured{Object: obj}
	_, err = nsres.Update(unObj, metav1.UpdateOptions{}, target.Name)
	if err != nil {
		log.Infof("Cannot update %s: %s: %v", target.Name, err, unObj)
	}
	return err
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
