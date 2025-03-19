package nodeutil

import (
	"fmt"

	ocphealthcheckv1 "github.com/barani129/ocphealthcheckinf/api/v1"
	ocphealthcheckutil "github.com/barani129/ocphealthcheckinf/internal/util"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	MACHINECONFIGUPDATEDONE           = "Done"
	MACHINECONFIGUPDATEDEGRADED       = "Degraded"
	MACHINECONFIGUPDATEINPROGRESS     = "Working"
	MACHINECONFIGUPDATEUNRECONCILABLE = "Unreconcilable"
	MACHINECONFIGUPDATEREBOOTING      = "Rebooting"
)

func OnNodeUpdate(newObj interface{}, spec *ocphealthcheckv1.OcpHealthCheckSpec, status *ocphealthcheckv1.OcpHealthCheckStatus, runningHost string, mcp *ocphealthcheckutil.MCPStruct) {
	node := new(corev1.Node)
	err := ocphealthcheckutil.ConvertUnStructureToStructured(newObj, node)
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
		if anno == "machineconfiguration.openshift.io/state" {
			if val != MACHINECONFIGUPDATEDONE {
				// assuming mcp update is in progress, check and report if it is failing
				if val == MACHINECONFIGUPDATEINPROGRESS || val == MACHINECONFIGUPDATEREBOOTING {
					// assuming mcp update is progressing without issues
					return
				} else if val == MACHINECONFIGUPDATEDEGRADED || val == MACHINECONFIGUPDATEUNRECONCILABLE {
					// assuming mcp udpate ran into issues and report
					ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", node.Name, "mcp-issue"), "faulty", fmt.Sprintf("node %s's mcp update is either degraded/unreconcilable in cluster %s, please execute <oc describe node %s> to validate it", node.Name, runningHost, node.Name), runningHost, spec)
					return
				}
			} else {
				ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", node.Name, "mcp-issue"), "recovered", fmt.Sprintf("node %s's mcp update which was previously degraded/unreconcilable is now done in cluster %s", node.Name, runningHost), runningHost, spec)
			}
		}
	}
	if node.Spec.Unschedulable {
		ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", node.Name, "sched"), "faulty", fmt.Sprintf("node %s has become unschedulable (No MCP update is in progress) in cluster %s, please execute <oc get nodes %s > to validate it", node.Name, runningHost, node.Name), runningHost, spec)
	} else {
		ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", node.Name, "sched"), "recovered", fmt.Sprintf("node %s which was previously unschedulable is now schedulable again in cluster %s", node.Name, runningHost), runningHost, spec)
	}
	for _, cond := range node.Status.Conditions {
		if cond.Type == "Ready" && cond.Status == "False" {
			ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", node.Name, "ready"), "faulty", fmt.Sprintf("node %s has become NotReady (No MCP update is in progress) in cluster %s, please execute <oc get nodes %s > to validate it", node.Name, runningHost, node.Name), runningHost, spec)
		} else if cond.Type == "Ready" && cond.Status == "True" {
			ocphealthcheckutil.SendEmail(fmt.Sprintf("/home/golanguser/files/.%s-%s.txt", node.Name, "ready"), "recovered", fmt.Sprintf("node %s which was previously marked as NotReady in cluster %s, please execute <oc get nodes %s > to validate it", node.Name, runningHost, node.Name), runningHost, spec)
		}
	}
}
