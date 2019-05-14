package controller

import (
	"errors"
	"reflect"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

	log.Infof("Replacing %s:%s", target.Resource.Key(), rule.TargetPath)
	if data == nil {
		log.Infof("Cannot replace %s:%s, no data from source",
			rule.Target.Key(), rule.TargetPath)
		return nil
	}

	res := c.dynclient.Resource(target.GVR)
	nsres := res.Namespace(target.Namespace)
	obj, err := nsres.Get(target.Name, metav1.GetOptions{})
	if err != nil {
		log.Infof("Cannot get %s: %s", target.Name, err)
		return err
	}

	// Update the target object.
	path := strings.Split(rule.TargetPath, ".")
	current, found, err := unstructured.NestedFieldNoCopy(obj.Object, path...)
	if err != nil {
		log.Infof("Cannot get field %s: %s", rule.TargetPath, err)
		return err
	}
	if found && reflect.DeepEqual(current, data) {
		log.Infof("Already equal")
		return nil
	}
	err = unstructured.SetNestedField(obj.Object, data, path...)
	if err != nil {
		log.Infof("Cannot set field %s: %s", rule.TargetPath, err)
		return err
	}

	// Run the update.
	_, err = nsres.Update(obj, metav1.UpdateOptions{})
	if err != nil {
		log.Infof("Cannot update %s: %s", target.Name, err)
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
