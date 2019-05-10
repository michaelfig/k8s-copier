package main

import (
	"context"

	log "k8s.io/klog"

	copier "github.com/michaelfig/k8s-copier/pkg/controller"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	ctrl "sigs.k8s.io/controller-runtime"
)

const (
	AppVersion = "v0.0.1"
)

type CopierControllerOptions struct {
	Targets    []string
	Namespaces []string
}

func NewCopierControllerOptions() *CopierControllerOptions {
	return &CopierControllerOptions{}
}

func (o *CopierControllerOptions) Register(mgr ctrl.Manager) error {
	ctx := context.TODO()
	c := copier.New(&ctx, mgr.GetConfig(), o.Namespaces)

	for _, target := range o.Targets {
		if err := c.AddTarget(target); err != nil {
			return err
		}
	}

	return mgr.Add(c)
}

func (o *CopierControllerOptions) AddFlags(fs *pflag.FlagSet) {
	// FIXME: Add from the actual flags.
	fs.StringSliceVarP(&o.Namespaces, "namespace", "n", []string{}, ""+
		"Specify the list of namespaces to act on."+
		" (default all namespaces)")
	fs.StringSliceVarP(&o.Targets, "target", "t", []string{}, ""+
		"Specify the target resource types to update (required)."+
		" Each must be {KIND|RESOURCE}[[.VERSION].GROUP]")
}

func NewCommandCopierController(stopCh <-chan struct{}) *cobra.Command {
	o := NewCopierControllerOptions()
	cmd := &cobra.Command{
		Use:   "k8s-copier",
		Short: "k8s-copier is a Kubernetes dynamic resource-to-resource copier",

		Run: func(cmd *cobra.Command, args []string) {
			log.Infof("starting k8s-copier %s", AppVersion)
			o.RunCopierController(stopCh)
		},
	}

	flags := cmd.Flags()
	o.AddFlags(flags)
	cmd.MarkFlagRequired("target")
	return cmd
}

func (o *CopierControllerOptions) RunCopierController(stopCh <-chan struct{}) {
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{})

	if err != nil {
		log.Fatalf("error creating manager: %v", err)
	}

	if err := o.Register(mgr); err != nil {
		log.Fatalf("error registering controller: %v", err)
	}

	if err := mgr.Start(stopCh); err != nil {
		log.Fatalf("error running manager: %v", err)
	}
}
