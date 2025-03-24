package util

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	ocpscanv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	nmstate "github.com/nmstate/kubernetes-nmstate/api/v1"
	cov1 "github.com/openshift/api/config/v1"
	mcfgv1 "github.com/openshift/api/machineconfiguration/v1"
	tunedv1 "github.com/openshift/cluster-node-tuning-operator/pkg/apis/tuned/v1"
	operatorframework "github.com/operator-framework/api/pkg/operators/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	mcluster "open-cluster-management.io/api/cluster/v1"
	ocmpolicy "open-cluster-management.io/governance-policy-propagator/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	MACHINECONFIGUPDATEDONE           = "Done"
	MACHINECONFIGUPDATEDEGRADED       = "Degraded"
	MACHINECONFIGUPDATEINPROGRESS     = "Working"
	MACHINECONFIGUPDATEUNRECONCILABLE = "Unreconcilable"
	MACHINECONFIGUPDATEREBOOTING      = "Rebooting"
	MACHINECONFIGDONEANNO             = "machineconfiguration.openshift.io/state"
	MCIMPORT                          = "ManagedClusterImportSucceeded"
	HUBACCEPT                         = "HubAcceptedManagedCluster"
	MCCOND                            = "ManagedClusterConditionAvailable"
	MCJOINED                          = "ManagedClusterJoined"
	MCCERT                            = "ClusterCertificateRotated"
	MCCLOCK                           = "ManagedClusterConditionClockSynced"
	ARGORUNNING                       = "Running"
	ARGOAVAILABLE                     = "Available"
	PODCRASHLOOP                      = "CrashLoopBackOff"
	PODRUNNING                        = "RUNNING"
	PODERRIMAGEPULL                   = "ErrImagePull"
	PODIMAGEPULLBACKOFF               = "ImagePullBackOff"
	NODEREADY                         = "Ready"
	NODEREADYFalse                    = "False"
	NODEREADYTrue                     = "True"
	MCPUpdating                       = "Updating"
	POLICYNONCOMPLIANT                = "NonCompliant"
	TUNEDAPPLIED                      = "Applied"
	READYUC                           = "READY"
	SUCCEEDED                         = "Succeeded"
)

var (
	SUCCESSCONDS = []string{MCCERT, MCCLOCK, MCCOND, MCJOINED, HUBACCEPT, MCIMPORT}
)

type OcpAPIConfig struct {
	APIServerArguments struct {
		AuditLogFormat         []string `json:"audit-log-format"`
		AuditLogMaxbackup      []string `json:"audit-log-maxbackup"`
		AuditLogMaxsize        []string `json:"audit-log-maxsize"`
		AuditLogPath           []string `json:"audit-log-path"`
		AuditPolicyFile        []string `json:"audit-policy-file"`
		EtcdHealthCheckTimeout []string `json:"etcd-HealthCheck-timeout"`
		EtcdReadycheckTimeout  []string `json:"etcd-readycheck-timeout"`
		FeatureGates           []string `json:"feature-gates"`
		ShutdownDelayDuration  []string `json:"shutdown-delay-duration"`
		ShutdownSendRetryAfter []string `json:"shutdown-send-retry-after"`
	} `json:"apiServerArguments"`
	APIServers struct {
		PerGroupOptions []any `json:"perGroupOptions"`
	} `json:"apiServers"`
	APIVersion    string `json:"apiVersion"`
	Kind          string `json:"kind"`
	ProjectConfig struct {
		ProjectRequestMessage string `json:"projectRequestMessage"`
	} `json:"projectConfig"`
	RoutingConfig struct {
		Subdomain string `json:"subdomain"`
	} `json:"routingConfig"`
	ServingInfo struct {
		BindNetwork   string   `json:"bindNetwork"`
		CipherSuites  []string `json:"cipherSuites"`
		MinTLSVersion string   `json:"minTLSVersion"`
	} `json:"servingInfo"`
	StorageConfig struct {
		Urls []string `json:"urls"`
	} `json:"storageConfig"`
}

type MCPStruct struct {
	IsMCPInProgress *bool
	IsNodeAffected  *bool
	MCPAnnoNode     string
	MCPAnnoState    string
	MCPNode         string
	MCPNodeState    string
}

func GetSpecAndStatus(ocpscan client.Object) (*ocpscanv1.OcpHealthCheckSpec, *ocpscanv1.OcpHealthCheckStatus, error) {
	switch ty := ocpscan.(type) {
	case *ocpscanv1.OcpHealthCheck:
		return &ty.Spec, &ty.Status, nil
	default:
		return nil, nil, fmt.Errorf("not an ocphealthscan type: %s", ty)
	}
}

func GetReadyCondition(status *ocpscanv1.OcpHealthCheckStatus) *ocpscanv1.OcpHealthCheckCondition {
	for _, c := range status.Conditions {
		if c.Type == ocpscanv1.OcpHealthCheckConditionReady {
			return &c
		}
	}
	return nil
}

func IsReady(status *ocpscanv1.OcpHealthCheckStatus) bool {
	if cond := GetReadyCondition(status); cond != nil {
		return cond.Status == ocpscanv1.ConditionTrue
	}
	return false
}

func SetReadyCondition(status *ocpscanv1.OcpHealthCheckStatus, conditionStatus ocpscanv1.ConditionStatus, reason string, message string) {
	readyCond := GetReadyCondition(status)
	if readyCond == nil {
		readyCond = &ocpscanv1.OcpHealthCheckCondition{
			Type: ocpscanv1.OcpHealthCheckConditionReady,
		}
		status.Conditions = append(status.Conditions, *readyCond)
	}
	if readyCond.Status != conditionStatus {
		readyCond.Status = conditionStatus
		now := metav1.Now()
		readyCond.LastTransitionTime = &now
	}
	readyCond.Reason = reason
	readyCond.Message = message
	for i, c := range status.Conditions {
		if c.Type == ocpscanv1.OcpHealthCheckConditionReady {
			status.Conditions[i] = *readyCond
			return
		}
	}
}

func GetApiName(clientset kubernetes.Clientset) (domain string, err error) {
	var ocpConfig *OcpAPIConfig
	cm, err := clientset.CoreV1().ConfigMaps("openshift-apiserver").Get(context.Background(), "config", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	data := cm.Data["config.yaml"]
	err = json.Unmarshal([]byte(data), &ocpConfig)
	if err != nil {
		return "", err
	}
	return ocpConfig.RoutingConfig.Subdomain, nil
}

func HandleCNString(cn string) string {
	var nonAlphanumericRegex = regexp.MustCompile(`[^a-zA-Z0-9 ]+`)
	return nonAlphanumericRegex.ReplaceAllString(cn, "")
}

func writeFile(filename string, data string) error {
	err := os.WriteFile(filename, []byte(data), 0666)
	if err != nil {
		return err
	}
	return nil
}

func ReadFile(filename string) (string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func SendEmailAlert(category string, nodeName string, filename string, spec *ocpscanv1.OcpHealthCheckSpec, alert string) {
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		message := fmt.Sprintf(`/usr/bin/printf '%s\n' "Subject: %s OcpHealthCheck alert from %s" "" "Alert: %s" | /usr/sbin/sendmail -f %s -S %s %s`, "%s", category, nodeName, alert, spec.Email, spec.RelayHost, spec.Email)
		cmd3 := exec.Command("/bin/bash", "-c", message)
		err := cmd3.Run()
		if err != nil {
			fmt.Printf("Failed to send the alert: %s", err)
		}
		writeFile(filename, "sent")
	} else {
		data, _ := ReadFile(filename)
		if data != "sent" {
			message := fmt.Sprintf(`/usr/bin/printf '%s\n' "Subject: %s OcpHealthCheck alert from %s" "" "Alert: %s" | /usr/sbin/sendmail -f %s -S %s %s`, "%s", category, nodeName, alert, spec.Email, spec.RelayHost, spec.Email)
			cmd3 := exec.Command("/bin/bash", "-c", message)
			err := cmd3.Run()
			if err != nil {
				fmt.Printf("Failed to send the alert: %s", err)
			}
			os.Truncate(filename, 0)
			writeFile(filename, "sent")
		}
	}
}

func SendEmailRecoveredAlert(category string, nodeName string, filename string, spec *ocpscanv1.OcpHealthCheckSpec, commandToRun string) {
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		//
	} else {
		data, err := ReadFile(filename)
		if err != nil {
			fmt.Printf("Failed to send the alert: %s", err)
		}
		if data == "sent" {
			message := fmt.Sprintf(`/usr/bin/printf '%s\n' "Subject: %s OcpHealthCheck alert from %s" ""  "Resolved: %s" | /usr/sbin/sendmail -f %s -S %s %s`, "%s", category, nodeName, commandToRun, spec.Email, spec.RelayHost, spec.Email)
			cmd3 := exec.Command("/bin/bash", "-c", message)
			err := cmd3.Run()
			if err != nil {
				fmt.Printf("Failed to send the alert: %s", err)
			}
			os.Remove(filename)
		}
	}
}

func GetAPIName(clientset kubernetes.Clientset) (domain string, err error) {
	var apiconfig OcpAPIConfig
	cm, err := clientset.CoreV1().ConfigMaps("openshift-apiserver").Get(context.Background(), "config", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	data := cm.Data["config.yaml"]
	err = json.Unmarshal([]byte(data), &apiconfig)
	if err != nil {
		return "", err
	}
	if apiconfig.RoutingConfig.Subdomain == "" {
		return "", fmt.Errorf("subdomain is empty")
	}
	return apiconfig.RoutingConfig.Subdomain, nil
}

func EnableMCP(mcp *MCPStruct, nodeName string, val string) {
	if mcp.IsNodeAffected != nil && !*mcp.IsNodeAffected {
		a := true
		mcp.IsNodeAffected = &a
		mcp.MCPNode = nodeName
		mcp.MCPNodeState = val
	}
}

func CheckSingleNodeReadiness(clientset *kubernetes.Clientset, nodeName string) (bool, error) {
	node, err := clientset.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	if node.Spec.Unschedulable {
		return true, nil
	}
	for _, cond := range node.Status.Conditions {
		if cond.Type == NODEREADY && cond.Status == NODEREADYFalse {
			return true, nil
		}
	}
	return false, nil
}

func CheckNodeReadiness(clientset *kubernetes.Clientset, nodeLabel map[string]string) (bool, string, error) {
	nodeList, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{
		LabelSelector: labels.Set(nodeLabel).String(),
	})
	if err != nil {
		return false, "", err
	}

	if len(nodeList.Items) > 0 {
		for _, node := range nodeList.Items {
			if node.Spec.Unschedulable {
				return true, node.Name, nil
			}
			for _, cond := range node.Status.Conditions {
				if cond.Type == NODEREADY && cond.Status == NODEREADYFalse {
					return true, node.Name, nil
				}
			}
		}
	}
	return false, "", nil
}

func CheckNodeMcpAnnotations(clientset *kubernetes.Clientset, nodeLabel map[string]string) (bool, string, error) {
	nodeList, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{
		LabelSelector: labels.Set(nodeLabel).String(),
	})
	if err != nil {
		return false, "", err
	}
	for _, node := range nodeList.Items {
		for anno, val := range node.Annotations {
			// to be updated
			if anno == MACHINECONFIGDONEANNO {
				if val != MACHINECONFIGUPDATEDONE {
					return true, node.Name, nil
				}
			}
		}
	}
	return false, "", nil
}

func ConvertUnStructureToStructured(oldObj interface{}, k8sobj interface{}) error {
	u := oldObj.(*unstructured.Unstructured)
	unstructuredJSON, err := u.MarshalJSON()
	if err != nil {
		return err
	}
	err = json.Unmarshal(unstructuredJSON, &k8sobj)
	if err != nil {
		return err
	}
	return nil
}

func PvHasDifferentNode(clientset *kubernetes.Clientset, pv string, podNode string) (bool, error) {
	volAttList, err := clientset.StorageV1().VolumeAttachments().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return false, nil
	}
	for _, volAtt := range volAttList.Items {
		if volAtt.Spec.Source.PersistentVolumeName != nil && *volAtt.Spec.Source.PersistentVolumeName != "" {
			if *volAtt.Spec.Source.PersistentVolumeName == pv {
				if volAtt.Spec.NodeName != podNode {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func SendEmail(category string, filename string, alertType string, alertString string, runningHost string, spec *ocpscanv1.OcpHealthCheckSpec) {
	if alertType == "faulty" {
		log.Log.Info(alertString)
	}
	if spec.SuspendEmailAlert != nil && !*spec.SuspendEmailAlert {
		if alertType == "faulty" {
			SendEmailAlert(category, runningHost, filename, spec, alertString)
		} else {
			SendEmailRecoveredAlert(category, runningHost, filename, spec, alertString)
		}
	}
}

func OnCoUpdate(newObj interface{}, clientset *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	co := new(cov1.ClusterOperator)
	err := ConvertUnStructureToStructured(newObj, co)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if co.DeletionTimestamp != nil {
		return
	}
	// check if actual mcp is in progress
	if mcp, err := CheckMCPINProgress(clientset); err != nil {
		return
	} else if err == nil && mcp {
		return
	}

	for _, cond := range co.Status.Conditions {
		if cond.Type == "Degraded" {
			if cond.Status == "True" {
				SendEmail("cluster-operator", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s.txt", co.Name), "faulty", fmt.Sprintf("Cluster operator %s is degraded and no actual mcp update is in progress in cluster %s, please execute <oc get co> to validate it", co.Name, runningHost), runningHost, spec)
			} else {
				SendEmail("cluster-operator", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s.txt", co.Name), "recovered", fmt.Sprintf("Cluster operator %s which was previously degraded is back to working state in cluster %s, please execute <oc get co> to validate it", co.Name, runningHost), runningHost, spec)
			}
		}
	}
}

func OnNNCPUpdate(newObj interface{}, clientset *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	nncp := new(nmstate.NodeNetworkConfigurationPolicy)
	err := ConvertUnStructureToStructured(newObj, nncp)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if nncp.DeletionTimestamp != nil {
		return
	}
	if mcp, err := CheckMCPINProgress(clientset); err != nil {
		return
	} else if err == nil && mcp {
		return
	}

	for _, cond := range nncp.Status.Conditions {
		if cond.Type == "Degraded" {
			if cond.Status == "True" {
				SendEmail("nncp", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s.txt", nncp.Name), "faulty", fmt.Sprintf("NNCP %s is degraded and no actual mcp update is in progress in cluster %s, please execute <oc get nncp> to validate it", nncp.Name, runningHost), runningHost, spec)
			} else {
				SendEmail("nncp", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s.txt", nncp.Name), "recovered", fmt.Sprintf("NNCP %s which was previously degraded is back to working state in cluster %s, please execute <oc get nncp> to validate it", nncp.Name, runningHost), runningHost, spec)
			}
		}
	}
}

func OnCatalogSourceUpdate(newObj interface{}, clientset *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	cs := new(operatorframework.CatalogSource)
	err := ConvertUnStructureToStructured(newObj, cs)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if cs.DeletionTimestamp != nil {
		return
	}
	if mcp, err := CheckMCPINProgress(clientset); err != nil {
		return
	} else if err == nil && mcp {
		return
	}
	if cs.Status.GRPCConnectionState.LastObservedState != "READY" {
		SendEmail("catalog-source", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s.txt", cs.Name), "faulty", fmt.Sprintf("CatalogSource %s's connection state is not READY and no actual mcp update is in progress in cluster %s, please execute <oc get catalogsources> to validate it", cs.Name, runningHost), runningHost, spec)
	} else {
		SendEmail("catalog-source", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s.txt", cs.Name), "recovered", fmt.Sprintf("CatalogSource %s's connection state which was previously NOTREADY is READY now in cluster %s, please execute <oc get catalogsources> to validate it", cs.Name, runningHost), runningHost, spec)
	}
}

func OnCsvUpdate(newObj interface{}, clientset *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	cs := new(operatorframework.ClusterServiceVersion)
	err := ConvertUnStructureToStructured(newObj, cs)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if cs.DeletionTimestamp != nil {
		return
	}
	if mcp, err := CheckMCPINProgress(clientset); err != nil {
		return
	} else if err == nil && mcp {
		return
	}
	if pol, err := IsChildPolicyNamespace(clientset, cs.Namespace); err != nil {
		return
	} else if err == nil && pol {
		return
	}

	if cs.Status.Phase != SUCCEEDED {
		SendEmail("ClusterServiceVersion", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s.txt", cs.Name), "faulty", fmt.Sprintf("CSV %s is either degraded/in-progress and no actual mcp update is in progress in cluster %s, please execute <oc get csv> to validate it", cs.Name, runningHost), runningHost, spec)
	} else {
		SendEmail("ClusterServiceVersion", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s.txt", cs.Name), "recovered", fmt.Sprintf("CSV %s which was previously degraded/in-progress is succeeded now in cluster %s, please execute <oc get csv> to validate it", cs.Name, runningHost), runningHost, spec)
	}
}

func OnOSIPSetUpdate(newObj interface{}) {

}

// policy functions
func OnPolicyAdd(newObj interface{}, spec *ocpscanv1.OcpHealthCheckSpec, status *ocpscanv1.OcpHealthCheckStatus) {
	policy := new(ocmpolicy.Policy)
	err := ConvertUnStructureToStructured(newObj, policy)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if !IsChildPolicy(policy) {
		log.Log.Info(fmt.Sprintf("New policy.open-cluster-management.io/v1 %s has been added to namespace %s", policy.Name, policy.Namespace))
	} else {
		log.Log.Info(fmt.Sprintf("New child policy %s has been added to namespace %s, possible CGU update is in progress, please check HUB cluster", policy.Name, policy.Namespace))

	}
}

func OnPolicyUpdate(newObj interface{}, staticClientSet *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, status *ocpscanv1.OcpHealthCheckStatus, runningHost string) {
	policy := new(ocmpolicy.Policy)
	err := ConvertUnStructureToStructured(newObj, policy)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if policy.DeletionTimestamp != nil {
		// assuming it is deletion, so will ignore it
		return
	}
	if !IsChildPolicy(policy) {
		if policy.Status.ComplianceState == POLICYNONCOMPLIANT || policy.Spec.Disabled {
			SendEmail("Policy-update", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", policy.Name, "noncomplaint"), "faulty", fmt.Sprintf("Root policy %s is either non-compliant/disabled in namespace %s in cluster %s, please execute <oc get policy %s -n %s -o json | jq .status> to validate it", policy.Name, policy.Namespace, runningHost, policy.Name, policy.Namespace), runningHost, spec)
		} else {

			SendEmail("Policy-update", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", policy.Name, "noncomplaint"), "recovered", fmt.Sprintf("Root policy %s which was previously non-compliant/disabled is now compliant/enabled again in namespace %s in cluster %s", policy.Name, policy.Namespace, runningHost), runningHost, spec)
		}
	} else {
		if ns, err := GetChildPolicyObjectNamespace(staticClientSet); err != nil {
			// ignoring errors
		} else if len(ns) > 0 {
			SendEmail("Policy-update", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", policy.Name, "child"), "faulty", fmt.Sprintf("Child policy %s is in progress in namespace %s, pod updates from namespace %s will be paused until CGU update is completed in cluster %s", policy.Name, policy.Namespace, ns[0], runningHost), runningHost, spec)
		}
	}
}

func OnPolicyDelete(oldObj interface{}, spec *ocpscanv1.OcpHealthCheckSpec, status *ocpscanv1.OcpHealthCheckStatus, runningHost string) {
	policy := new(ocmpolicy.Policy)
	err := ConvertUnStructureToStructured(oldObj, policy)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if IsChildPolicy(policy) {
		SendEmail("Policy-update", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", policy.Name, "child"), "recovered", fmt.Sprintf("Child policy %s has been deleted in namespace %s, pod updates from the objectDefinition namespace will continue. Possible CGU complete in cluster %s", policy.Name, policy.Namespace, runningHost), runningHost, spec)
	} else {
		SendEmail("Policy-update", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", policy.Name, "noncomplaint"), "recovered", fmt.Sprintf("Root policy %s which was previously non-compliant/disabled is now deleted in namespace %s in cluster %s", policy.Name, policy.Namespace, runningHost), runningHost, spec)
	}
}

func IsChildPolicy(policy *ocmpolicy.Policy) bool {
	for labelName, _ := range policy.Labels {
		if labelName == "openshift-cluster-group-upgrades/parentPolicyName" {
			return true
		}
	}
	return false
}

func IsChildPolicyNamespace(clientset *kubernetes.Clientset, ns string) (bool, error) {
	policyNamespaces, err := GetChildPolicyObjectNamespace(clientset)
	if err != nil {
		return false, err
	}
	if len(policyNamespaces) > 0 {
		if slices.Contains(policyNamespaces, ns) {
			return true, nil
		}
	}
	return false, nil
}

func GetChildPolicyObjectNamespace(clientset *kubernetes.Clientset) ([]string, error) {
	policies := ocmpolicy.PolicyList{}
	err := clientset.RESTClient().Get().AbsPath("/apis/policy.open-cluster-management.io/v1/policies").Do(context.Background()).Into(&policies)
	if err != nil {
		return nil, err
	}
	var affectedNS []string
	if len(policies.Items) > 0 {
		for _, policy := range policies.Items {
			if IsChildPolicy(&policy) {
				for _, temp := range policy.Spec.PolicyTemplates {
					obj, _, err := unstructured.UnstructuredJSONScheme.Decode(temp.ObjectDefinition.Raw, nil, nil)
					if err != nil {
						return nil, err
					}
					unObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
					if err != nil {
						return nil, err
					}

					spec := unObj["spec"].(map[string]interface{})
					objTemp := spec["object-templates"].([]interface{})
					if len(objTemp) > 0 {
						for _, def := range objTemp {
							oDef := def.(map[string]interface{})
							if len(oDef) > 0 {
								objDef := oDef["objectDefinition"].(map[string]interface{})
								if len(objDef) > 0 {
									meta := objDef["metadata"].(map[string]interface{})
									for k, v := range meta {
										if k == "namespace" {
											affectedNS = append(affectedNS, v.(string))
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
	return nil, nil
}

// MCP functions
func OnMCPUpdate(newObj interface{}, staticClientSet *kubernetes.Clientset, status *ocpscanv1.OcpHealthCheckStatus, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	mcp := new(mcfgv1.MachineConfigPool)
	err := ConvertUnStructureToStructured(newObj, mcp)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if spec.HubCluster != nil && *spec.HubCluster {
		if mcp.Spec.Paused {
			if mcpInProgress, node, err := CheckNodeMcpAnnotations(staticClientSet, mcp.Spec.NodeSelector.MatchLabels); err != nil {
				// exiting
				return
			} else if err == nil && mcpInProgress {
				SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp-pause", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s is paused and actual update is in progress and node %s's annotation has been set to other than done in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, node, runningHost, mcp.Name), runningHost, spec)
				return
			} else {
				SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp-pause", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s is paused but no actual MCP update in progress in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, runningHost, mcp.Name), runningHost, spec)
			}
		} else {
			SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp-pause", mcp.Name), "recovered", fmt.Sprintf("MachineConfig pool %s is unpaused in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, runningHost, mcp.Name), runningHost, spec)
		}
	}

	for _, cond := range mcp.Status.Conditions {
		if cond.Type == MCPUpdating {
			if cond.Status == NODEREADYTrue {
				// Check node annotations to validate it
				if mcp.Spec.MachineConfigSelector.MatchLabels != nil {
					if isMcPInProgress, node, err := CheckNodeMcpAnnotations(staticClientSet, mcp.Spec.NodeSelector.MatchLabels); err != nil {
						return
					} else if err == nil && isMcPInProgress {
						SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s update is in progress and node %s's annotation has been set to other than done in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, node, runningHost, mcp.Name), runningHost, spec)
						return
					} else {
						if isNodeAffected, anode, err := CheckNodeReadiness(staticClientSet, mcp.Spec.MachineConfigSelector.MatchLabels); err != nil {
							// unable to verify node status
							return
						} else if err == nil && isNodeAffected {
							SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s update has been set to true, due to possible manual action on node %s in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, anode, runningHost, mcp.Name), runningHost, spec)
						} else {
							SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s update has been set to true, nodes are healthy, mcp update is probably just starting in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, runningHost, mcp.Name), runningHost, spec)
						}
					}
				}
			} else if cond.Status == NODEREADYFalse {
				SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp", mcp.Name), "recovered", fmt.Sprintf("MachineConfig pool %s update which was previously set to true is now changed to false in cluster %s, please execute <oc get mcp %s > to validate it", mcp.Name, runningHost, mcp.Name), runningHost, spec)
			}
		} else if cond.Type == MACHINECONFIGUPDATEDEGRADED {
			if cond.Status == NODEREADYTrue {
				SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp", mcp.Name), "faulty", fmt.Sprintf("MachineConfig pool %s is degraded in cluster %s, please execute <oc get mcp %s and oc get nodes> to validate it", mcp.Name, runningHost, mcp.Name), runningHost, spec)
			} else if cond.Status == "False" {
				SendEmail("MachineConfigPool", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "mcp", mcp.Name), "recovered", fmt.Sprintf("MachineConfig pool %s is no longer degraded in cluster %s", mcp.Name, runningHost), runningHost, spec)
			}
		}
	}
}

func CheckMCPINProgress(clientset *kubernetes.Clientset) (bool, error) {
	mcpList := mcfgv1.MachineConfigPoolList{}
	err := clientset.RESTClient().Get().AbsPath("/apis/machineconfiguration.openshift.io/v1/machineconfigpools").Do(context.Background()).Into(&mcpList)
	if err != nil {
		return false, err
	}
	for _, mcp := range mcpList.Items {
		for _, cond := range mcp.Status.Conditions {
			if cond.Type == MCPUpdating {
				if cond.Status == NODEREADYTrue {
					if mcpInProgress, _, err := CheckNodeMcpAnnotations(clientset, mcp.Spec.NodeSelector.MatchLabels); err != nil {
						return false, err
					} else if err == nil && mcpInProgress {
						return true, nil
					}
					// ignored else condition as mcp update true condition could have caused by manual update
				}
			}
		}
	}
	return false, nil
}

// Node functions
func OnNodeUpdate(newObj interface{}, spec *ocpscanv1.OcpHealthCheckSpec, status *ocpscanv1.OcpHealthCheckStatus, runningHost string) {
	node := new(corev1.Node)
	err := ConvertUnStructureToStructured(newObj, node)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if node.DeletionTimestamp != nil {
		//assuming deletion, ignoring it
		return
	}
	for anno, val := range node.Annotations {
		// to be updated
		if anno == MACHINECONFIGDONEANNO {
			if val != MACHINECONFIGUPDATEDONE {
				// assuming mcp update is in progress, check and report if it is failing
				if val == MACHINECONFIGUPDATEINPROGRESS || val == MACHINECONFIGUPDATEREBOOTING {
					// assuming mcp update is progressing without issues
					return
				} else if val == MACHINECONFIGUPDATEDEGRADED || val == MACHINECONFIGUPDATEUNRECONCILABLE {
					// assuming mcp udpate ran into issues and report
					SendEmail("Node", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", node.Name, "mcp-issue"), "faulty", fmt.Sprintf("node %s's mcp update is either degraded/unreconcilable in cluster %s, please execute <oc describe node %s> to validate it", node.Name, runningHost, node.Name), runningHost, spec)
					return
				}
			} else {
				SendEmail("Node", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", node.Name, "mcp-issue"), "recovered", fmt.Sprintf("node %s's mcp update which was previously degraded/unreconcilable is now done in cluster %s", node.Name, runningHost), runningHost, spec)
			}
		}
	}
	if node.Spec.Unschedulable {
		SendEmail("Node", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", node.Name, "sched"), "faulty", fmt.Sprintf("node %s has become unschedulable (No MCP update is in progress) in cluster %s, please execute <oc get nodes %s > to validate it", node.Name, runningHost, node.Name), runningHost, spec)
		return
	} else {
		SendEmail("Node", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", node.Name, "sched"), "recovered", fmt.Sprintf("node %s which was previously unschedulable is now schedulable again in cluster %s", node.Name, runningHost), runningHost, spec)
	}
	for _, cond := range node.Status.Conditions {
		if cond.Type == NODEREADY && cond.Status == NODEREADYFalse {
			SendEmail("Node", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", node.Name, "ready"), "faulty", fmt.Sprintf("node %s has become NotReady (No MCP update is in progress) in cluster %s, please execute <oc get nodes %s > to validate it", node.Name, runningHost, node.Name), runningHost, spec)
		} else if cond.Type == NODEREADY && cond.Status == NODEREADYTrue {
			SendEmail("Node", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", node.Name, "ready"), "recovered", fmt.Sprintf("node %s which was previously marked as NotReady in cluster %s, please execute <oc get nodes %s > to validate it", node.Name, runningHost, node.Name), runningHost, spec)
		}
	}
}

// pod functions
func OnPodDelete(oldObj interface{}, clientset *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, status *ocpscanv1.OcpHealthCheckStatus, runningHost string) {
	po := new(corev1.Pod)
	err := ConvertUnStructureToStructured(oldObj, po)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if mcp, err := CheckMCPINProgress(clientset); err != nil {
		return
	} else if err == nil && mcp {
		return
	}
	SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", po.Name, po.Namespace), "recovered", fmt.Sprintf("pod %s's container which was previously terminated/CrashLoopBackOff is now deleted in namespace %s in cluster %s ", po.Name, po.Namespace, runningHost), runningHost, spec)
}

func OnPodAdd(oldObj interface{}) {
	po := new(corev1.Pod)
	err := ConvertUnStructureToStructured(oldObj, po)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	log.Log.Info(fmt.Sprintf("pod %s has been added to namespace %s", po.Name, po.Namespace))
}

func PodLastRestartTimerUp(timeStr string) bool {
	oldTime, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return false
	}
	var timePast bool
	currTime := time.Now().Add(-30 * time.Minute)
	timePast = oldTime.Before(currTime)
	return timePast
}

func CleanUpRunningPods(clientset *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, status *ocpscanv1.OcpHealthCheckStatus, runningHost string) {
	podList, err := clientset.CoreV1().Pods(corev1.NamespaceAll).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		log.Log.Info(fmt.Sprintf("unable to retrieve pods due to error %s", err.Error()))
	}
	if files, err := os.ReadDir("/home/golanguser/files/ocphealth/"); err != nil {
		log.Log.Info(err.Error())
	} else {
		for _, file := range files {
			if len(podList.Items) > 0 {
				for _, pod := range podList.Items {
					if strings.Contains(file.Name(), fmt.Sprintf(".%s-%s.txt", pod.Name, pod.Namespace)) {
						for _, cont := range pod.Status.ContainerStatuses {
							if cont.State.Running != nil || (cont.State.Terminated != nil && cont.State.Terminated.ExitCode == 0) {
								if cont.RestartCount > 0 {
									if PodLastRestartTimerUp(cont.LastTerminationState.Terminated.FinishedAt.String()) {
										SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", pod.Name, pod.Namespace), "recovered", fmt.Sprintf("pod %s which was previously waiting/terminated with non exit code 0 is now either running/completed in namespace %s in cluster %s ", pod.Name, pod.Namespace, runningHost), runningHost, spec)
									}
								} else {
									SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", pod.Name, pod.Namespace), "recovered", fmt.Sprintf("pod %s which was previously waiting/terminated with non exit code 0 is now either running/completed in namespace %s in cluster %s ", pod.Name, pod.Namespace, runningHost), runningHost, spec)
								}

							}
						}
					}
				}
			}
		}
	}
	if files, err := os.ReadDir("/home/golanguser/files/ocphealth/"); err != nil {
		if len(files) < 1 {
			status.Healthy = true
		} else {
			status.Healthy = false
		}
	}
}

func OnPodUpdate(newObj interface{}, spec *ocpscanv1.OcpHealthCheckSpec, status *ocpscanv1.OcpHealthCheckStatus, runningHost string, clientset *kubernetes.Clientset) {
	if !strings.Contains(runningHost, "ospctl") {
		evnfmHost := ""
		evnfmPort := "443"
		if err := CheckEVNFMConnectivity(evnfmHost, evnfmPort); err != nil {
			SendEmail("EVNFM-Connectivity", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", evnfmHost, evnfmPort), "faulty", fmt.Sprintf("EVNFM %s on port %s is unreachable from cluster %s ", evnfmHost, evnfmPort, runningHost), runningHost, spec)
		} else {
			SendEmail("EVNFM-Connectivity", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", evnfmHost, evnfmPort), "recovered", fmt.Sprintf("EVNFM %s on port %s is now reachable again from cluster %s ", evnfmHost, evnfmPort, runningHost), runningHost, spec)
		}
	}
	newPo := new(corev1.Pod)
	err := ConvertUnStructureToStructured(newObj, newPo)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if newPo.DeletionTimestamp != nil {
		// assuming it is deletion, so ignoring
		return
	}
	if mcp, err := CheckMCPINProgress(clientset); err != nil {
		log.Log.Info("unable to retrieve MCP progress")
		return
	} else if err == nil && mcp {
		log.Log.Info("MCP in progress")
		return
	}
	// ignoring pod changes during node restart
	if nodeAffected, err := CheckSingleNodeReadiness(clientset, newPo.Spec.NodeName); err != nil {
		log.Log.Info("unable to retrieve node information")
		return
	} else if nodeAffected {
		log.Log.Info("Exiting as node is not-ready/unschedulable")
		return
	}

	if sameNs, err := IsChildPolicyNamespace(clientset, newPo.Namespace); err != nil {
		log.Log.Info("unable to retrieve policy object namespace")
		return
	} else if sameNs {
		log.Log.Info("Exiting as child policy update is in progress")
		// SendEmail("Pod", fmt.Sprintf("cgu update in progress for namespace %s", newPo.Namespace), "faulty", fmt.Sprintf("possible CGU update is in progress for objects in namespace %s in cluster %s, no pod update alerts will be sent until CGU is compliant, please execute <oc get pods -n %s and oc get policy -A> to validate", newPo.Namespace, runningHost, newPo.Namespace), runningHost, spec)
		return
	}

	for _, newCont := range newPo.Status.ContainerStatuses {
		if newCont.State.Terminated != nil && newCont.State.Terminated.ExitCode != 0 {
			SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("pod %s's container %s is terminated with non exit code 0 in namespace %s in cluster %s", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
		} else if newCont.State.Running != nil || (newCont.State.Terminated != nil && newCont.State.Terminated.ExitCode == 0) {
			// Assuming if pod has moved back to running from CrashLoopBackOff/others, the restart count will always be greater than 0
			if newCont.RestartCount > 0 {
				if newCont.LastTerminationState.Terminated != nil && newCont.LastTerminationState.Terminated.ExitCode != 0 && newCont.LastTerminationState.Terminated.FinishedAt.String() != "" {
					if PodLastRestartTimerUp(newCont.LastTerminationState.Terminated.FinishedAt.String()) {
						SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("pod %s's container %s whic was previously terminate with non exit code 0 is now either running/completed in namespace %s in cluster %s", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
					}
				}
			}
		}
		if newCont.State.Waiting != nil {
			if newCont.State.Waiting.Reason == "CrashLoopBackOff" {
				if len(newPo.Spec.Volumes) > 0 {
					for _, vol := range newPo.Spec.Volumes {
						if vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName != "" {
							// Get the persistent volume claim name
							pvc, err := clientset.CoreV1().PersistentVolumeClaims(newPo.Namespace).Get(context.Background(), vol.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
							if err != nil {
								if k8serrors.IsNotFound(err) {
									SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, configured PVC %s doesn't exist in namespace %s in cluster %s, please execute <oc get pods %s -n %s -o json | jq .spec.volumes[] and oc get pvc %s -n %s -o json | jq .spec.volumeName> to validate it", newPo.Name, newCont.Name, pvc.Name, pvc.Namespace, runningHost, newPo.Name, newPo.Namespace, pvc.Name, pvc.Namespace), runningHost, spec)
								} else {
									SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, unable to retrieve configured PVC %s in namespace %s in cluster %s, please execute <oc get pods %s -n %s -o json | jq .spec.volumes[] and oc get pvc %s -n %s -o json | jq .spec.volumeName> to validate it", newPo.Name, newCont.Name, pvc.Name, pvc.Namespace, runningHost, newPo.Name, newPo.Namespace, pvc.Name, pvc.Namespace), runningHost, spec)
								}
							}
							if affected, err := PvHasDifferentNode(clientset, pvc.Spec.VolumeName, newPo.Spec.NodeName); err != nil {
								SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, unable to retrieve volume attachment of volume %s in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc get volumeattachments | grep %s > to validate it", newPo.Name, newCont.Name, pvc.Spec.VolumeName, runningHost, newPo.Name, newPo.Namespace, pvc.Spec.VolumeName), runningHost, spec)
							} else if err == nil && affected {
								SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, volume attachment of volume %s is mounted on a different node in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc get volumeattachments | grep %s > to validate it", newPo.Name, newCont.Name, pvc.Spec.VolumeName, runningHost, newPo.Name, newPo.Namespace, pvc.Spec.VolumeName), runningHost, spec)
							} else {
								// Check if it is due to other issues
								for _, cont := range newPo.Spec.Containers {
									if cont.Name == newCont.Name {
										if newCont.Image != cont.Image {
											SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, volume attachment of volume %s is mounted on the SAME node, could be other issues in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc get volumeattachments | grep %s > to validate it", newPo.Name, newCont.Name, pvc.Spec.VolumeName, runningHost, newPo.Name, newPo.Namespace, pvc.Spec.VolumeName), runningHost, spec)
										}
									}
								}
							}
						} else {
							for _, cont := range newPo.Spec.Containers {
								if cont.Name == newCont.Name {
									if newCont.Image != cont.Image {
										SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, appears to be ErrImagePull error in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses and oc  > to validate it", newPo.Name, newCont.Name, runningHost, newPo.Name, newPo.Namespace), runningHost, spec)
									} else {
										SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("Pod %s's container %s is in CrashLoopBackOff state, no persistent volume is attached to the pod, doesn't seem to be ErrImagePull, could be other issues, in cluster %s, please execute <oc get pods %s -n %s -o json | jq .status.containerStatuses> to validate it", newPo.Name, newCont.Name, runningHost, newPo.Name, newPo.Namespace), runningHost, spec)
									}
								}
							}
						}
					}
				}
			} else if newCont.State.Waiting.Reason == PODERRIMAGEPULL || newCont.State.Waiting.Reason == "ImagePullBackOff" {
				SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "faulty", fmt.Sprintf("pod %s's container %s is failing in namespace %s due to ErrImagePull in cluster %s", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
			}
		} else {
			// Assuming if pod has moved back to running from CrashLoopBackOff/others, the restart count will always be greater than 0
			if newCont.RestartCount > 0 {
				if newCont.LastTerminationState.Terminated != nil && newCont.LastTerminationState.Terminated.ExitCode != 0 && newCont.LastTerminationState.Terminated.FinishedAt.String() != "" {
					if PodLastRestartTimerUp(newCont.LastTerminationState.Terminated.FinishedAt.String()) {
						SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "recovered", fmt.Sprintf("pod %s's container %s which was previously terminated with non exit code 0 is now either running/completed in namespace %s in cluster %s ", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
					}
				} else if newCont.State.Terminated != nil && newCont.State.Terminated.ExitCode == 0 {
					// this condition will satisfy the pod that was previously running/completed and went into issues (due to image pull for example) and becomes running/completed
					SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "recovered", fmt.Sprintf("pod %s's container %s which was previously terminated with non exit code 0 is now either running/completed in namespace %s in cluster %s ", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
				}
			} else {
				// this condition will satisfy the pod that was never in running state (due to image pull for example) and becomes running/completed
				if newCont.State.Terminated == nil {
					SendEmail("Pod", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", newPo.Name, newPo.Namespace), "recovered", fmt.Sprintf("pod %s's container %s which was previously terminated/waiting is now either running/completed in namespace %s in cluster %s ", newPo.Name, newCont.Name, newPo.Namespace, runningHost), runningHost, spec)
				}
			}
		}
	}
}

func CheckEVNFMConnectivity(host string, port string) error {
	command := fmt.Sprintf("/usr/bin/nc -w 3 -zv %s %s", host, port)
	cmd := exec.Command("/bin/bash", "-c", command)
	err := cmd.Run()
	if err != nil {
		return err
	}
	return nil
}

// Hub functions

func OnManagedClusterUpdate(newObj interface{}, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	mcl := new(mcluster.ManagedCluster)
	err := ConvertUnStructureToStructured(newObj, mcl)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	if mcl.DeletionTimestamp != nil {
		// ignore deletion
		return
	}
	for _, cond := range mcl.Status.Conditions {
		if slices.Contains(SUCCESSCONDS, cond.Type) {
			if cond.Status != NODEREADYTrue {
				SendEmail("ManagedCluster", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", mcl.Name, "cond"), "faulty", fmt.Sprintf("ManagedCluster %s's condition %s is set to false in cluster %s, please execute <oc get managedcluster %s> to validate it", mcl.Name, cond.Type, runningHost, mcl.Name), runningHost, spec)
			} else {
				SendEmail("ManagedCluster", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", mcl.Name, "cond"), "recovered", fmt.Sprintf("ManagedCluster %s's condition %s is set back to true in cluster %s, please execute <oc get managedcluster %s> to validate it", mcl.Name, cond.Type, runningHost, mcl.Name), runningHost, spec)
			}
		}
	}
}

func OnTunedProfileUpdate(newObj interface{}, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	tp := new(tunedv1.Profile)
	err := ConvertUnStructureToStructured(newObj, tp)
	if err != nil {
		log.Log.Error(err, "unable to convert")
		return
	}
	if tp.DeletionTimestamp != nil {
		return
	}
	for _, cond := range tp.Status.Conditions {
		if (cond.Type == TUNEDAPPLIED && cond.Status == NODEREADYFalse) || (cond.Type == MACHINECONFIGUPDATEDEGRADED && cond.Status == NODEREADYTrue) {
			SendEmail("TunedProfile", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "tuned-profile", tp.Name), "faulty", fmt.Sprintf("TunedProfile %s's status condition in node %s is either degraded or not-applied in cluster %s, please execute <oc get profiles.tuned.openshift.io %s -n %s -o json | jq .status> to validate it", tp.Status.TunedProfile, tp.Name, runningHost, tp.Name, tp.Namespace), runningHost, spec)
		} else {
			SendEmail("TunedProfile", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", "tuned-profile", tp.Name), "recovered", fmt.Sprintf("TunedProfile %s's status condition in node %s is recovered in cluster %s, please execute <oc get tunedprofile %s -n %s -o json | jq .status> to validate it", tp.Status.TunedProfile, tp.Name, runningHost, tp.Name, tp.Namespace), runningHost, spec)
		}
	}
}
