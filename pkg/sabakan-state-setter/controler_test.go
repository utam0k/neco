package sss

import (
	"context"
	"testing"
	"time"

	sabakan "github.com/cybozu-go/sabakan/v2"

	serf "github.com/hashicorp/serf/client"
)

func newMockController(gql *gqlMockClient, metricsInput string, mt *machineType) *Controller {
	return &Controller{
		interval:      time.Minute,
		parallelSize:  2,
		sabakanClient: gql,
		prom:          newMockPromClient(metricsInput),
		machineTypes:  []*machineType{mt},
		machineStateSources: []*MachineStateSource{
			{
				serial: "00000001",
				ipv4:   "10.0.0.100",
				serfStatus: &serf.Member{
					Status: "alive",
					Tags: map[string]string{
						systemdUnitsFailedTag: "",
					},
				},
				machineType: mt,
				metrics:     map[string]machineMetrics{},
			},
		},
	}

}

func TestControllerRun(t *testing.T) {
	t.Parallel()

	machineTypeQEMU := &machineType{
		Name: "qemu",
		MetricsCheckList: []targetMetric{
			{
				Name: "hw_processor_status_health",
			},
			{
				Name: "hw_storage_controller_status_health",
				Selector: &selector{
					Labels: map[string]string{
						"controller": "PCIeSSD.Slot.2-C",
						"system":     "System.Embedded.1",
					},
				},
			},
			{
				Name: "hw_storage_controller_status_health",
				Selector: &selector{
					Labels: map[string]string{"controller": "PCIeSSD.Slot.3-C"},
				},
			},
			{
				Name: "hw_storage_controller_status_health",
				Selector: &selector{
					LabelPrefix: map[string]string{
						"controller": "SATAHDD.Slot.",
						"system":     "System.Embedded.",
					},
				},
				MinimumHealthyCount: intPointer(1),
			},
		},
	}

	// transition machine state to unhealthy due to cpu warning
	gql := newMockGQLClient("qemu")
	metricsInput := `
	hw_processor_status_health{processor="CPU.Socket.1"} 0
	hw_processor_status_health{processor="CPU.Socket.2"} 1
	`
	ctr := newMockController(gql, metricsInput, machineTypeQEMU)
	err := ctr.run(context.Background())
	if err != nil {
		t.Error(err)
	}
	if gql.machine.Status.State != sabakan.MachineState(sabakan.StateUnhealthy.GQLEnum()) {
		t.Errorf("machine is not unhealthy: %s", gql.machine.Status.State)
	}

	// transition machine state to unhealthy due to warning disks become larger than one
	gql = newMockGQLClient("qemu")
	metricsInput = `
	hw_processor_status_health{processor="CPU.Socket.1"} 0
	hw_processor_status_health{processor="CPU.Socket.2"} 0
	hw_storage_controller_status_health{controller="SATAHDD.Slot.1"} 1
	hw_storage_controller_status_health{controller="SATAHDD.Slot.2"} 1
	`
	ctr = newMockController(gql, metricsInput, machineTypeQEMU)
	err = ctr.run(context.Background())
	if err != nil {
		t.Error(err)
	}
	if gql.machine.Status.State != sabakan.MachineState(sabakan.StateUnhealthy.GQLEnum()) {
		t.Errorf("machine is not unhealthy: %s", gql.machine.Status.State)
	}

	// transition machine state to healthy even one disk warning occurred
	gql = newMockGQLClient("qemu")
	metricsInput = `
	hw_processor_status_health{processor="CPU.Socket.1"} 0
	hw_processor_status_health{processor="CPU.Socket.2"} 0
	hw_storage_controller_status_health{controller="PCIeSSD.Slot.2-C", system="System.Embedded.1"} 0
	hw_storage_controller_status_health{controller="PCIeSSD.Slot.3-C", system="System.Embedded.1"} 0
	hw_storage_controller_status_health{controller="SATAHDD.Slot.1", system="System.Embedded.1"} 0
	hw_storage_controller_status_health{controller="SATAHDD.Slot.2", system="System.Embedded.1"} 1
	`
	ctr = newMockController(gql, metricsInput, machineTypeQEMU)
	err = ctr.run(context.Background())
	if err != nil {
		t.Error(err)
	}
	if gql.machine.Status.State != sabakan.MachineState(sabakan.StateHealthy.GQLEnum()) {
		t.Errorf("machine is not healthy: %s", gql.machine.Status.State)
	}
}
