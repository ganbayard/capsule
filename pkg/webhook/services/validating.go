// Copyright 2020-2021 Clastix Labs
// SPDX-License-Identifier: Apache-2.0

package services

import (
	"context"
	"net"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/clastix/capsule/api/v1alpha1"
	capsulewebhook "github.com/clastix/capsule/pkg/webhook"
)

// +kubebuilder:webhook:path=/validating-external-service-ips,mutating=false,sideEffects=None,admissionReviewVersions=v1,failurePolicy=fail,groups="",resources=services,verbs=create;update,versions=v1,name=validating-external-service-ips.capsule.clastix.io

const (
	enableNodePortsAnnotation = "capsule.clastix.io/enable-node-ports"
)

type webhook struct {
	handler capsulewebhook.Handler
}

func Webhook(handler capsulewebhook.Handler) capsulewebhook.Webhook {
	return &webhook{handler: handler}
}

func (w *webhook) GetHandler() capsulewebhook.Handler {
	return w.handler
}

func (w *webhook) GetName() string {
	return "Service"
}

func (w *webhook) GetPath() string {
	return "/validating-external-service-ips"
}

type handler struct{}

func Handler() capsulewebhook.Handler {
	return &handler{}
}

func (r *handler) handleService(ctx context.Context, clt client.Client, decoder *admission.Decoder, req admission.Request, recorder record.EventRecorder) admission.Response {
	svc := &corev1.Service{}
	if err := decoder.Decode(req, svc); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	tntList := &v1alpha1.TenantList{}
	if err := clt.List(ctx, tntList, client.MatchingFieldsSelector{
		Selector: fields.OneTermEqualSelector(".status.namespaces", svc.GetNamespace()),
	}); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if len(tntList.Items) == 0 {
		return admission.Allowed("")
	}
	tnt := tntList.Items[0]

	if svc.Spec.Type == corev1.ServiceTypeNodePort && tnt.GetAnnotations()[enableNodePortsAnnotation] == "false" {
		recorder.Eventf(&tnt, corev1.EventTypeWarning, "ForbiddenNodePort", "Service %s/%s cannot be type of NodePort for the current Tenant", req.Namespace, req.Name)

		return admission.Errored(http.StatusBadRequest, NewNodePortDisabledError())
	}

	if svc.Spec.ExternalIPs == nil || tnt.Spec.ExternalServiceIPs == nil {
		return admission.Allowed("")
	}

	ipInCIDR := func(ip net.IP) bool {
		for _, allowed := range tnt.Spec.ExternalServiceIPs.Allowed {
			if !strings.Contains(string(allowed), "/") {
				allowed += "/32"
			}

			_, allowedIP, _ := net.ParseCIDR(string(allowed))

			if allowedIP.Contains(ip) {
				return true
			}
		}
		return false
	}

	for _, externalIP := range svc.Spec.ExternalIPs {
		ip := net.ParseIP(externalIP)

		if !ipInCIDR(ip) {
			recorder.Eventf(&tnt, corev1.EventTypeWarning, "ForbiddenExternalServiceIP", "Service %s/%s external IP %s is forbidden for the current Tenant", req.Namespace, req.Name, ip.String())

			return admission.Errored(http.StatusBadRequest, NewExternalServiceIPForbidden(tnt.Spec.ExternalServiceIPs.Allowed))
		}
	}

	return admission.Allowed("")
}

func (r *handler) OnCreate(client client.Client, decoder *admission.Decoder, recorder record.EventRecorder) capsulewebhook.Func {
	return func(ctx context.Context, req admission.Request) admission.Response {
		return r.handleService(ctx, client, decoder, req, recorder)
	}
}

func (r *handler) OnUpdate(client client.Client, decoder *admission.Decoder, recorder record.EventRecorder) capsulewebhook.Func {
	return func(ctx context.Context, req admission.Request) admission.Response {
		return r.handleService(ctx, client, decoder, req, recorder)
	}
}

func (r *handler) OnDelete(client.Client, *admission.Decoder, record.EventRecorder) capsulewebhook.Func {
	return func(ctx context.Context, req admission.Request) admission.Response {
		return admission.Allowed("")
	}
}
