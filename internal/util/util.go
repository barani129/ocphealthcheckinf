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
	MCPNode         map[string]string
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
		message := fmt.Sprintf(`/usr/bin/printf '%s\n' "Subject: MetallbScan alert from %s" "" "Alert: %s" | /usr/sbin/sendmail -f %s -S %s %s`, "%s", nodeName, alert, spec.Email, spec.RelayHost, spec.Email)
		cmd3 := exec.Command("/bin/bash", "-c", message)
		err := cmd3.Run()
		if err != nil {
			fmt.Printf("Failed to send the alert: %s", err)
		}
		writeFile(filename, "sent")
	} else {
		data, _ := ReadFile(filename)
		if data != "sent" {
			message := fmt.Sprintf(`/usr/bin/printf '%s\n' "Subject: MetallbScan alert from %s" "" "Alert: %s" | /usr/sbin/sendmail -f %s -S %s %s`, "%s", nodeName, alert, spec.Email, spec.RelayHost, spec.Email)
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

func CheckNodeReadiness(clientset *kubernetes.Clientset, nodeLabel map[string]string) (bool, map[string]string, error) {
	nodeList, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{
		LabelSelector: labels.Set(nodeLabel).String(),
	})
	if err != nil {
		return false, nil, err
	}
	var affectedNode map[string]string
	for _, node := range nodeList.Items {
		for _, cond := range node.Status.Conditions {
			if cond.Type == "Ready" && cond.Status == "True" {
				affectedNode[node.Name] = "NotReady"
				return true, affectedNode, nil
			}
		}
		if node.Spec.Unschedulable {
			affectedNode[node.Name] = "unschedulable"
			return true, affectedNode, nil
		}
	}
	return false, nil, nil
}

func CheckNodeMcpAnnotations(clientset *kubernetes.Clientset, nodeLabel map[string]string) (bool, map[string]string, error) {
	nodeList, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{
		LabelSelector: labels.Set(nodeLabel).String(),
	})
	if err != nil {
		return false, nil, err
	}
	var affectedNode map[string]string
	for _, node := range nodeList.Items {
		for anno, val := range node.Annotations {
			// to be updated
			if anno == "machineconfiguration.openshift.io/state" {
				if val != MACHINECONFIGUPDATEDONE {
					affectedNode[node.Name] = val
					return true, affectedNode, nil
				}
			}
		}
	}
	return false, nil, nil
}

func ConvertUnStructureToStructured(oldObj interface{}, k8sobj interface{}) (err error) {
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
	}
	log.Log.Info(fmt.Sprintf("pod %s has been added to namespace %s", po.Name, po.Namespace))
}

func OnPodUpdate(newObj interface{}) {
	newPo := new(corev1.Pod)
	err := ConvertUnStructureToStructured(newObj, newPo)
	if err != nil {
		log.Log.Error(err, "failed to convert")
	}
	for _, newCont := range newPo.Status.ContainerStatuses {
		if newCont.State.Terminated != nil && newCont.State.Terminated.ExitCode != 0 {
			log.Log.Info(fmt.Sprintf("pod %s is terminated with non exit code 0 in namespace %s", newPo.Name, newPo.Namespace))
		}
		if newCont.State.Waiting != nil {
			if newCont.State.Waiting.Reason == "CrashLoopBackOff" {
				log.Log.Info(fmt.Sprintf("pod %s is in waiting with state %s in namespace %s", newPo.Name, newCont.State.Waiting.Reason, newPo.Namespace))
			} else if newCont.State.Waiting.Reason == "ErrImagePull" {
				log.Log.Info(fmt.Sprintf("pod %s is in waiting with state %s in namespace %s", newPo.Name, newCont.State.Waiting.Reason, newPo.Namespace))
			}
		}
	}
}

func OnNodeUpdate(newObj interface{}) {
	node := new(corev1.Node)
	err := ConvertUnStructureToStructured(newObj, node)
	if err != nil {
		log.Log.Error(err, "failed to convert")
	}
	if node.Spec.Unschedulable {
		log.Log.Info(fmt.Sprintf("Node %s has become unschedulable, please check", node.Name))
	}
}
