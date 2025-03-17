package util

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"

	ocpscanv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
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

const (
	MACHINECONFIGUPDATEDONE = "Done"
)

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

func SendEmailAlert(nodeName string, filename string, spec *ocpscanv1.OcpHealthCheckSpec, alert string) {
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		message := fmt.Sprintf(`/usr/bin/printf '%s\n' "Subject: OcpHealthCheck alert from %s" "" "Alert: %s" | /usr/sbin/sendmail -f %s -S %s %s`, "%s", nodeName, alert, spec.Email, spec.RelayHost, spec.Email)
		cmd3 := exec.Command("/bin/bash", "-c", message)
		err := cmd3.Run()
		if err != nil {
			fmt.Printf("Failed to send the alert: %s", err)
		}
		writeFile(filename, "sent")
	} else {
		data, _ := ReadFile(filename)
		if data != "sent" {
			message := fmt.Sprintf(`/usr/bin/printf '%s\n' "Subject: OcpHealthCheck alert from %s" "" "Alert: %s" | /usr/sbin/sendmail -f %s -S %s %s`, "%s", nodeName, alert, spec.Email, spec.RelayHost, spec.Email)
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

func SendEmailRecoveredAlert(nodeName string, filename string, spec *ocpscanv1.OcpHealthCheckSpec, commandToRun string) {
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		//
	} else {
		data, err := ReadFile(filename)
		if err != nil {
			fmt.Printf("Failed to send the alert: %s", err)
		}
		if data == "sent" {
			message := fmt.Sprintf(`/usr/bin/printf '%s\n' "Subject: OcpHealthCheck alert from %s" ""  "Resolved: %s" | /usr/sbin/sendmail -f %s -S %s %s`, "%s", nodeName, commandToRun, spec.Email, spec.RelayHost, spec.Email)
			cmd3 := exec.Command("/bin/bash", "-c", message)
			err := cmd3.Run()
			if err != nil {
				fmt.Printf("Failed to send the alert: %s", err)
			}
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

func EnableMCPAnno(mcp *MCPStruct, nodeName string, val string) {
	if mcp.IsMCPInProgress != nil && !*mcp.IsMCPInProgress {
		a := true
		mcp.IsMCPInProgress = &a
		mcp.MCPAnnoNode = nodeName
		mcp.MCPAnnoState = val
	}
}

func DisableMCPAnno(mcp *MCPStruct) {
	if mcp.IsMCPInProgress != nil && !*mcp.IsMCPInProgress {
		a := false
		mcp.IsMCPInProgress = &a
		mcp.MCPAnnoNode = ""
		mcp.MCPAnnoState = ""
	}
}

func DisableMCP(mcp *MCPStruct) {
	if mcp.IsNodeAffected != nil && *mcp.IsNodeAffected {
		a := false
		mcp.IsNodeAffected = &a
		mcp.MCPNode = ""
		mcp.MCPNodeState = ""
	}
}

func CheckNodeReadiness(clientset *kubernetes.Clientset, nodeLabel map[string]string, mcp *MCPStruct) error {
	nodeList, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{
		LabelSelector: labels.Set(nodeLabel).String(),
	})
	if err != nil {
		return err
	}

	if len(nodeList.Items) > 0 {
		for _, node := range nodeList.Items {
			if node.Spec.Unschedulable {
				EnableMCP(mcp, node.Name, "unschedulable")
				return nil
			}
			for _, cond := range node.Status.Conditions {
				if cond.Type == "Ready" && cond.Status == "False" {
					EnableMCP(mcp, node.Name, "NotReady")
					return nil
				}
			}
		}
	}
	return nil
}

func CheckNodeMcpAnnotations(clientset *kubernetes.Clientset, nodeLabel map[string]string, mcp *MCPStruct) error {
	nodeList, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{
		LabelSelector: labels.Set(nodeLabel).String(),
	})
	if err != nil {
		return err
	}
	for _, node := range nodeList.Items {
		for anno, val := range node.Annotations {
			// to be updated
			if anno == "machineconfiguration.openshift.io/state" {
				if val != MACHINECONFIGUPDATEDONE {
					EnableMCPAnno(mcp, node.Name, val)
					return nil
				} else {
					DisableMCPAnno(mcp)
				}
			}
		}
	}
	return nil
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

func OnPodAdd(oldObj interface{}) {
	po := new(corev1.Pod)
	err := ConvertUnStructureToStructured(oldObj, po)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	log.Log.Info(fmt.Sprintf("pod %s has been added to namespace %s", po.Name, po.Namespace))
}

func OnPodDelete(oldObj interface{}) {
	po := new(corev1.Pod)
	err := ConvertUnStructureToStructured(oldObj, po)
	if err != nil {
		log.Log.Error(err, "failed to convert")
		return
	}
	log.Log.Info(fmt.Sprintf("pod %s has been deleted from namespace %s", po.Name, po.Namespace))
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
