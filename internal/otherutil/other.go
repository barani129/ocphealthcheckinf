package otherutil

import (
	"fmt"

	ocphealthcheckv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	"github.com/barani129/ocphealthcheckinf/internal/mcputil"
	"github.com/barani129/ocphealthcheckinf/internal/policyutil"
	ocphealthcheckutil "github.com/barani129/ocphealthcheckinf/internal/util"
	nmstate "github.com/nmstate/kubernetes-nmstate/api/v1"
	cov1 "github.com/openshift/api/config/v1"
	operatorframework "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func OnCoUpdate(newObj interface{}, clientset *kubernetes.Clientset, spec *ocphealthcheckv1.OcpHealthCheckSpec, runningHost string) {
	co := new(cov1.ClusterOperator)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, co)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if co.DeletionTimestamp != nil {
		return
	}
	// check if actual mcp is in progress
	if mcp, err := mcputil.CheckMCPINProgress(clientset); err != nil {
		return
	} else if err == nil && mcp {
		return
	}

	for _, cond := range co.Status.Conditions {
		if cond.Type == "Degraded" {
			if cond.Status == "True" {
				ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s.txt", co.Name), "faulty", fmt.Sprintf("Cluster operator %s is degraded and no actual mcp update is in progress in cluster %s, please execute <oc get co> to validate it", co.Name, runningHost), runningHost, spec)
			} else {
				ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s.txt", co.Name), "recovered", fmt.Sprintf("Cluster operator %s which was previously degraded is back to working state in cluster %s, please execute <oc get co> to validate it", co.Name, runningHost), runningHost, spec)
			}
		}
	}
}

func OnNNCPUpdate(newObj interface{}, clientset *kubernetes.Clientset, spec *ocphealthcheckv1.OcpHealthCheckSpec, runningHost string) {
	nncp := new(nmstate.NodeNetworkConfigurationPolicy)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, nncp)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if nncp.DeletionTimestamp != nil {
		return
	}
	if mcp, err := mcputil.CheckMCPINProgress(clientset); err != nil {
		return
	} else if err == nil && mcp {
		return
	}

	for _, cond := range nncp.Status.Conditions {
		if cond.Type == "Degraded" {
			if cond.Status == "True" {
				ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s.txt", nncp.Name), "faulty", fmt.Sprintf("NNCP %s is degraded and no actual mcp update is in progress in cluster %s, please execute <oc get nncp> to validate it", nncp.Name, runningHost), runningHost, spec)
			} else {
				ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s.txt", nncp.Name), "recovered", fmt.Sprintf("NNCP %s which was previously degraded is back to working state in cluster %s, please execute <oc get nncp> to validate it", nncp.Name, runningHost), runningHost, spec)
			}
		}
	}
}

func OnCatalogSourceUpdate(newObj interface{}, clientset *kubernetes.Clientset, spec *ocphealthcheckv1.OcpHealthCheckSpec, runningHost string) {
	cs := new(operatorframework.CatalogSource)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, cs)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if cs.DeletionTimestamp != nil {
		return
	}
	if mcp, err := mcputil.CheckMCPINProgress(clientset); err != nil {
		return
	} else if err == nil && mcp {
		return
	}
	if cs.Status.GRPCConnectionState.LastObservedState != "READY" {
		ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s.txt", cs.Name), "faulty", fmt.Sprintf("CatalogSource %s's connection state is not READY and no actual mcp update is in progress in cluster %s, please execute <oc get catalogsources> to validate it", cs.Name, runningHost), runningHost, spec)
	} else {
		ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s.txt", cs.Name), "recovered", fmt.Sprintf("CatalogSource %s's connection state which was previously NOTREADY is READY now in cluster %s, please execute <oc get catalogsources> to validate it", cs.Name, runningHost), runningHost, spec)
	}
}

func OnCsvUpdate(newObj interface{}, clientset *kubernetes.Clientset, spec *ocphealthcheckv1.OcpHealthCheckSpec, runningHost string) {
	cs := new(operatorframework.ClusterServiceVersion)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, cs)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if cs.DeletionTimestamp != nil {
		return
	}
	if mcp, err := mcputil.CheckMCPINProgress(clientset); err != nil {
		return
	} else if err == nil && mcp {
		return
	}
	if pol, err := policyutil.IsChildPolicyNamespace(clientset, cs.Namespace); err != nil {
		return
	} else if err == nil && pol {
		return
	}

	if cs.Status.Phase != "Succeeded" {
		ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s.txt", cs.Name), "faulty", fmt.Sprintf("CSV %s is either degraded/in-progress in namespace %s and no actual mcp update is in progress in cluster %s, please execute <oc get csv -n %s> to validate it", cs.Name, cs.Namespace, runningHost, cs.Namespace), runningHost, spec)
	} else {
		ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s.txt", cs.Name), "recovered", fmt.Sprintf("CSV %s which was previously degraded/in-progress is succeeded now in namespace %s in cluster %s, please execute <oc get csv -n %s> to validate it", cs.Name, cs.Namespace, runningHost, cs.Namespace), runningHost, spec)
	}
}
