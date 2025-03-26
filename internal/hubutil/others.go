package hubutil

import (
	"context"
	"fmt"

	argoop "github.com/barani129/argocd-operator/api/v1beta1"
	ocpscanv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	ocphealthcheckutil "github.com/barani129/ocphealthcheckinf/internal/util"
	nmstate "github.com/nmstate/kubernetes-nmstate/api/v1"
	cov1 "github.com/openshift/api/config/v1"
	tunedv1 "github.com/openshift/cluster-node-tuning-operator/pkg/apis/tuned/v1"
	operatorframework "github.com/operator-framework/api/pkg/operators/v1alpha1"
	"k8s.io/client-go/kubernetes"
	mcluster "open-cluster-management.io/api/cluster/v1"
)

func OnCoUpdate(clientset *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	coList := cov1.ClusterOperatorList{}
	err := clientset.RESTClient().Get().AbsPath("/apis/config.openshift.io/v1/clusteroperators").Do(context.Background()).Into(&coList)
	if err != nil {
		return
	}
	for _, co := range coList.Items {
		if co.DeletionTimestamp != nil {
			return
		}
		// check if actual mcp is in progress
		if mcp, err := ocphealthcheckutil.CheckMCPINProgress(clientset); err != nil {
			return
		} else if err == nil && mcp {
			return
		}
		for _, cond := range co.Status.Conditions {
			if cond.Type == "Degraded" {
				if cond.Status == "True" {
					ocphealthcheckutil.SendEmail("cluster-operator", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s.txt", co.Name), "faulty", fmt.Sprintf("Cluster operator %s is degraded and no actual mcp update is in progress in cluster %s, please execute <oc get co> to validate it", co.Name, runningHost), runningHost, spec)
				} else {
					ocphealthcheckutil.SendEmail("cluster-operator", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s.txt", co.Name), "recovered", fmt.Sprintf("Cluster operator %s which was previously degraded is back to working state in cluster %s, please execute <oc get co> to validate it", co.Name, runningHost), runningHost, spec)
				}
			}
		}
	}
}

func OnNNCPUpdate(clientset *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	if mcp, err := ocphealthcheckutil.CheckMCPINProgress(clientset); err != nil {
		return
	} else if err == nil && mcp {
		return
	}
	nncpList := nmstate.NodeNetworkConfigurationPolicyList{}
	err := clientset.RESTClient().Get().AbsPath("/apis/nmstate.io/v1/nodenetworkconfigurationpolicies").Do(context.Background()).Into(&nncpList)
	if err != nil {
		return
	}
	for _, nncp := range nncpList.Items {
		if nncp.DeletionTimestamp != nil {
			return
		}
		for _, cond := range nncp.Status.Conditions {
			if cond.Type == "Degraded" {
				if cond.Status == "True" {
					ocphealthcheckutil.SendEmail("nncp", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s.txt", nncp.Name), "faulty", fmt.Sprintf("NNCP %s is degraded and no actual mcp update is in progress in cluster %s, please execute <oc get nncp> to validate it", nncp.Name, runningHost), runningHost, spec)
				} else {
					ocphealthcheckutil.SendEmail("nncp", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s.txt", nncp.Name), "recovered", fmt.Sprintf("NNCP %s which was previously degraded is back to working state in cluster %s, please execute <oc get nncp> to validate it", nncp.Name, runningHost), runningHost, spec)
				}
			}
		}
	}
}

func OnCatalogSourceUpdate(clientset *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	if mcp, err := ocphealthcheckutil.CheckMCPINProgress(clientset); err != nil {
		return
	} else if err == nil && mcp {
		return
	}
	csList := operatorframework.CatalogSourceList{}
	err := clientset.RESTClient().Get().AbsPath("/apis/operators.coreos.com/v1alpha1/catalogsources").Do(context.Background()).Into(&csList)
	if err != nil {
		return
	}
	for _, cs := range csList.Items {
		if cs.DeletionTimestamp != nil {
			return
		}
		if pol, err := ocphealthcheckutil.IsChildPolicyNamespace(clientset, cs.Namespace); err != nil {
			return
		} else if err == nil && pol {
			return
		}
		if cs.Status.GRPCConnectionState.LastObservedState != ocphealthcheckutil.READYUC {
			ocphealthcheckutil.SendEmail("catalog-source", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s.txt", cs.Name), "faulty", fmt.Sprintf("CatalogSource %s's connection state is not READY and no actual mcp update is in progress in cluster %s, please execute <oc get catalogsources> to validate it", cs.Name, runningHost), runningHost, spec)
		} else {
			ocphealthcheckutil.SendEmail("catalog-source", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s.txt", cs.Name), "recovered", fmt.Sprintf("CatalogSource %s's connection state which was previously NOTREADY is READY now in cluster %s, please execute <oc get catalogsources> to validate it", cs.Name, runningHost), runningHost, spec)
		}
	}
}

func OnCsvUpdate(clientset *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	if mcp, err := ocphealthcheckutil.CheckMCPINProgress(clientset); err != nil {
		return
	} else if err == nil && mcp {
		return
	}
	csList := operatorframework.ClusterServiceVersionList{}
	err := clientset.RESTClient().Get().AbsPath("/apis/operators.coreos.com/v1alpha1/clusterserviceversions").Do(context.Background()).Into(&csList)
	if err != nil {
		return
	}
	for _, cs := range csList.Items {
		if cs.DeletionTimestamp != nil {
			return
		}
		if cs.Status.Phase != ocphealthcheckutil.SUCCEEDED {
			ocphealthcheckutil.SendEmail("ClusterServiceVersion", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s.txt", cs.Name), "faulty", fmt.Sprintf("CSV %s is either degraded/in-progress and no actual mcp update is in progress in cluster %s, please execute <oc get csv> to validate it", cs.Name, runningHost), runningHost, spec)
		} else {
			ocphealthcheckutil.SendEmail("ClusterServiceVersion", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s.txt", cs.Name), "recovered", fmt.Sprintf("CSV %s which was previously degraded/in-progress is succeeded now in cluster %s, please execute <oc get csv> to validate it", cs.Name, runningHost), runningHost, spec)
		}
	}
}

func OnManagedClusterUpdate(clientset *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	mclList := mcluster.ManagedClusterList{}
	err := clientset.RESTClient().Get().AbsPath("/apis/cluster.open-cluster-management.io/v1/managedclusters").Do(context.Background()).Into(&mclList)
	if err != nil {
		return
	}
	for _, mcl := range mclList.Items {
		if mcl.DeletionTimestamp != nil {
			// ignore deletion
			return
		}
		for _, cond := range mcl.Status.Conditions {
			if cond.Status != ocphealthcheckutil.NODEREADYTrue {
				ocphealthcheckutil.SendEmail("ManagedCluster", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", mcl.Name, cond.Type), "faulty", fmt.Sprintf("ManagedCluster %s's condition %s is set to false in cluster %s, please execute <oc get managedcluster %s -o json | jq .status> to validate it", mcl.Name, cond.Type, runningHost, mcl.Name), runningHost, spec)
			} else {
				ocphealthcheckutil.SendEmail("ManagedCluster", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", mcl.Name, cond.Type), "recovered", fmt.Sprintf("ManagedCluster %s's condition %s is set back to true in cluster %s, please execute <oc get managedcluster %s -o json | jq .status> to validate it", mcl.Name, cond.Type, runningHost, mcl.Name), runningHost, spec)
			}
		}
	}
}

func OnArgoUpdate(clientset *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	argocdList := argoop.ArgoCDList{}
	err := clientset.RESTClient().Get().AbsPath("/apis/argoproj.io/v1beta1/argocds").Do(context.Background()).Into(&argocdList)
	if err != nil {
		return
	}
	for _, argocd := range argocdList.Items {
		if argocd.DeletionTimestamp != nil {
			return
		}
		if argocd.Status.ApplicationController != ocphealthcheckutil.ARGORUNNING || argocd.Status.Phase != ocphealthcheckutil.ARGOAVAILABLE || argocd.Status.Redis != ocphealthcheckutil.ARGORUNNING || argocd.Status.Repo != ocphealthcheckutil.ARGORUNNING || argocd.Status.Server != ocphealthcheckutil.ARGORUNNING || argocd.Status.SSO != ocphealthcheckutil.ARGORUNNING {
			ocphealthcheckutil.SendEmail("ArgoCD", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", argocd.Name, argocd.Namespace), "faulty", fmt.Sprintf("ArgoCD %s's status condition is either not-running/unavailable in namespace %s of cluster %s, please execute <oc get argocd %s -n %s -o json | jq .status> to validate it", argocd.Name, argocd.Namespace, runningHost, argocd.Name, argocd.Namespace), runningHost, spec)
		} else {
			ocphealthcheckutil.SendEmail("ArgoCD", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", argocd.Name, argocd.Namespace), "recovered", fmt.Sprintf("ArgoCD %s's status condition is back to working condition in namespace %s of cluster %s, please execute <oc get argocd %s -n %s -o json | jq .status> to validate it", argocd.Name, argocd.Namespace, runningHost, argocd.Name, argocd.Namespace), runningHost, spec)
		}
	}
}

func OnTunedProfileUpdate(clientset *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	tpList := tunedv1.ProfileList{}
	err := clientset.RESTClient().Get().AbsPath("/apis/tuned.openshift.io/v1/profiles").Do(context.Background()).Into(&tpList)
	if err != nil {
		return
	}
	for _, tp := range tpList.Items {
		if tp.DeletionTimestamp != nil {
			return
		}
		for _, cond := range tp.Status.Conditions {
			if (cond.Type == ocphealthcheckutil.TUNEDAPPLIED && cond.Status == ocphealthcheckutil.NODEREADYFalse) || (cond.Type == ocphealthcheckutil.MACHINECONFIGUPDATEDEGRADED && cond.Status == ocphealthcheckutil.NODEREADYTrue) {
				ocphealthcheckutil.SendEmail("TunedProfile", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", tp.Name, tp.Namespace), "faulty", fmt.Sprintf("TunedProfile %s's status condition in node %s is either degraded or not-applied in cluster %s, please execute <oc get profiles.tuned.openshift.io %s -n %s -o json | jq .status> to validate it", tp.Status.TunedProfile, tp.Name, runningHost, tp.Name, tp.Namespace), runningHost, spec)
			} else {
				ocphealthcheckutil.SendEmail("TunedProfile", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", tp.Name, tp.Namespace), "recovered", fmt.Sprintf("TunedProfile %s's status condition in node %s is recovered in cluster %s, please execute <oc get tunedprofile %s -n %s -o json | jq .status> to validate it", tp.Status.TunedProfile, tp.Name, runningHost, tp.Name, tp.Namespace), runningHost, spec)
			}
		}
	}
}
