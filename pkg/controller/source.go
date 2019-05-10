package controller

import (
	"errors"
	"strings"
)

type ResourceSource struct {
	Spec *Resource
	Path string
}

type Source interface {
	Data() (interface{}, error)
	Add(*Controller, <-chan struct{}, *Rule, *ResourceInstance) error
}

func ParseSource(source, defaultNamespace string) (Source, error) {
	if jsonData := strings.TrimPrefix(source, "json:"); jsonData != source {
		return nil, errors.New("FIXME: JSON source not implemented")
	}
	split := strings.SplitN(source, ":", 3)
	nsplit := strings.SplitN(split[1], "/", 2)
	var res *Resource
	if len(nsplit) > 1 {
		res = &Resource{
			Kind:      split[0],
			Namespace: nsplit[0],
			Name:      nsplit[1],
		}
	} else {
		res = &Resource{
			Kind:      split[0],
			Namespace: defaultNamespace,
			Name:      nsplit[0],
		}
	}
	return &ResourceSource{
		Spec: res,
		Path: split[2],
	}, nil
}
