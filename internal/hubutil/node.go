package hubutil

import (
	"context"
	"fmt"

	ocpscanv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	ocphealthcheckutil "github.com/barani129/ocphealthcheckinf/internal/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func OnNodeUpdate(clientset *kubernetes.Clientset, spec *ocpscanv1.OcpHealthCheckSpec, runningHost string) {
	nodeList, err := clientset.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		log.Log.Error(err, "unable to retrieve node list")
		return
	}
	for _, node := range nodeList.Items {
		if node.DeletionTimestamp == nil {
			for anno, val := range node.Annotations {
				// to be updated
				if anno == ocphealthcheckutil.MACHINECONFIGDONEANNO {
					if val != ocphealthcheckutil.MACHINECONFIGUPDATEDONE {
						// assuming mcp update is in progress, check and report if it is failing
						if val == ocphealthcheckutil.MACHINECONFIGUPDATEINPROGRESS || val == ocphealthcheckutil.MACHINECONFIGUPDATEREBOOTING {
							// assuming mcp update is progressing without issues
							return
						} else if val == ocphealthcheckutil.MACHINECONFIGUPDATEDEGRADED || val == ocphealthcheckutil.MACHINECONFIGUPDATEUNRECONCILABLE {
							// assuming mcp udpate ran into issues and report
							ocphealthcheckutil.SendEmail("Node", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", node.Name, "mcp-issue"), "faulty", fmt.Sprintf("node %s's mcp update is either degraded/unreconcilable in cluster %s, please execute <oc describe node %s> to validate it", node.Name, runningHost, node.Name), runningHost, spec)
							return
						}
					} else {
						ocphealthcheckutil.SendEmail("Node", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", node.Name, "mcp-issue"), "recovered", fmt.Sprintf("node %s's mcp update which was previously degraded/unreconcilable is now done in cluster %s", node.Name, runningHost), runningHost, spec)
					}
				}
			}
			if node.Spec.Unschedulable {
				ocphealthcheckutil.SendEmail("Node", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", node.Name, "sched"), "faulty", fmt.Sprintf("node %s has become unschedulable (No MCP update is in progress) in cluster %s, please execute <oc get nodes %s > to validate it", node.Name, runningHost, node.Name), runningHost, spec)
				return
			} else {
				ocphealthcheckutil.SendEmail("Node", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", node.Name, "sched"), "recovered", fmt.Sprintf("node %s which was previously unschedulable is now schedulable again in cluster %s", node.Name, runningHost), runningHost, spec)
			}
			for _, cond := range node.Status.Conditions {
				if cond.Type == ocphealthcheckutil.NODEREADY && cond.Status == ocphealthcheckutil.NODEREADYFalse {
					ocphealthcheckutil.SendEmail("Node", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", node.Name, "ready"), "faulty", fmt.Sprintf("node %s has become NotReady (No MCP update is in progress) in cluster %s, please execute <oc get nodes %s > to validate it", node.Name, runningHost, node.Name), runningHost, spec)
				} else if cond.Type == ocphealthcheckutil.NODEREADY && cond.Status == ocphealthcheckutil.NODEREADYTrue {
					ocphealthcheckutil.SendEmail("Node", fmt.Sprintf("/home/golanguser/files/ocphealth/.%s-%s.txt", node.Name, "ready"), "recovered", fmt.Sprintf("node %s which was previously marked as NotReady in cluster %s, please execute <oc get nodes %s > to validate it", node.Name, runningHost, node.Name), runningHost, spec)
				}
			}
		}

	}

}
