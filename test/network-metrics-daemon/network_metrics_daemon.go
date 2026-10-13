package networkmetrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"

	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/openshift/network-metrics-daemon/test/utils/client"
	"github.com/openshift/network-metrics-daemon/test/utils/consts"
	"github.com/openshift/network-metrics-daemon/test/utils/namespaces"
	"github.com/openshift/network-metrics-daemon/test/utils/prometheus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	baseURL     = "http://localhost:9090"
	workerLabel = "node-role.kubernetes.io/worker="
)

var _ = ginkgo.Describe("NetworkMetricsDaemon", func() {
	ginkgo.BeforeEach(func() {
		err := namespaces.Clean(consts.TestingNamespace, client.Client)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	})

	ginkgo.Context("Network interface metrics", func() {
		ginkgo.BeforeEach(func() {
			ginkgo.By("creating the pod")

			metricsPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "metricpod",
					Namespace: consts.TestingNamespace,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "c1",
							Image:   consts.TestImage,
							Command: []string{"/bin/bash", "-c", "sleep inf"},
						},
					},
				},
			}

			metricsPod, err := client.Client.Pods(consts.TestingNamespace).Create(
				context.Background(),
				metricsPod,
				metav1.CreateOptions{},
			)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			ginkgo.By("waiting for the pod to be ready")
			gomega.Eventually(func() corev1.PodPhase {
				podObj, err := client.Client.Pods(consts.TestingNamespace).Get(context.Background(), metricsPod.Name, metav1.GetOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				return podObj.Status.Phase
			}, 5*time.Minute, 5*time.Second).Should(gomega.Equal(corev1.PodRunning))
		})

		ginkgo.It("should expose the network name for the Pod's default interface", func() {
			query := fmt.Sprintf("pod_network_name_info{namespace=\"%s\",pod=\"metricpod\",interface=\"eth0\"}", consts.TestingNamespace)
			url := fmt.Sprintf("%s/api/v1/query?%s", baseURL, (url.Values{"query": []string{query}}).Encode())

			queryPrometheusEventually(url, 5*time.Minute, 5*time.Second, func(queryOutput prometheus.Reply) bool {
				if len(queryOutput.Data.Results) != 1 {
					return false
				}

				result := queryOutput.Data.Results[0]
				return result.Metric.Namespace == consts.TestingNamespace &&
					result.Metric.Pod == "metricpod" &&
					result.Metric.Interface == "eth0" &&
					result.Metric.NetworkName != "" &&
					result.Value[1] == "0"
			})
		})
	})

	ginkgo.Context("Network Name metric", func() {
		// workerName is used to create all the pods explicitly on the same worker node
		var workerName string = ""

		ginkgo.BeforeEach(func() {
			if workerName == "" {
				workerList, err := client.Client.Nodes().List(context.Background(), metav1.ListOptions{LabelSelector: workerLabel})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(len(workerList.Items)).To(gomega.BeNumerically(">", 0))
				workerName = workerList.Items[0].Name
			}

			networkAttachmentDefinition0 := &nettypes.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nad0",
					Namespace: consts.TestingNamespace,
				},
				Spec: nettypes.NetworkAttachmentDefinitionSpec{
					Config: `{
								"cniVersion": "0.3.0",
								"type": "macvlan",
								"mode": "bridge",
								"ipam": {
									"type": "host-local",
									"ranges": [
										[ {
											"subnet": "192.168.200.0/24",
											"rangeStart": "192.168.200.10",
											"rangeEnd": "192.168.200.200"
										} ]
									]
								}
							}`,
				},
			}
			networkAttachmentDefinition1 := &nettypes.NetworkAttachmentDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nad1",
					Namespace: consts.TestingNamespace,
				},
				Spec: nettypes.NetworkAttachmentDefinitionSpec{
					Config: `{
								"cniVersion": "0.3.0",
								"type": "macvlan",
								"mode": "bridge",
								"ipam": {
									"type": "host-local",
									"ranges": [
										[ {
											"subnet": "192.168.202.0/24",
											"rangeStart": "192.168.202.10",
											"rangeEnd": "192.168.202.200"
										} ]
									]
								}
							}`,
				},
			}

			_, err := client.Client.NetworkAttachmentDefinitions(consts.TestingNamespace).Create(
				context.Background(),
				networkAttachmentDefinition0,
				metav1.CreateOptions{},
			)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			_, err = client.Client.NetworkAttachmentDefinitions(consts.TestingNamespace).Create(
				context.Background(),
				networkAttachmentDefinition1,
				metav1.CreateOptions{},
			)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"k8s.v1.cni.cncf.io/networks": fmt.Sprintf("%s/nad0, %s/nad1", consts.TestingNamespace, consts.TestingNamespace),
					},
					Name:      "metricpod",
					Namespace: consts.TestingNamespace,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "c1",
							Image:   consts.TestImage,
							Command: []string{"/bin/bash", "-c", "sleep inf"},
						},
					},
					NodeName: workerName,
				},
			}

			pod, err = client.Client.Pods(consts.TestingNamespace).Create(
				context.Background(),
				pod,
				metav1.CreateOptions{},
			)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			gomega.Eventually(func() corev1.PodPhase {
				podObj, err := client.Client.Pods(consts.TestingNamespace).Get(context.Background(), pod.Name, metav1.GetOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				return podObj.Status.Phase
			}, 5*time.Minute, 5*time.Second).Should(gomega.Equal(corev1.PodRunning))
		})

		ginkgo.It("should expose the network name for every Pod interface", func() {
			query := fmt.Sprintf("(pod_network_name_info{namespace=\"%s\",pod=\"metricpod\"})", consts.TestingNamespace)
			url := fmt.Sprintf("%s/api/v1/query?%s", baseURL, (url.Values{"query": []string{query}}).Encode())

			queryPrometheusEventually(url, 5*time.Minute, 5*time.Second, func(queryOutput prometheus.Reply) bool {
				results := queryOutput.Data.Results
				if len(results) != 3 {
					return false
				}

				expectedNetworks := map[string]string{
					"net1": fmt.Sprintf("%s/nad0", consts.TestingNamespace),
					"net2": fmt.Sprintf("%s/nad1", consts.TestingNamespace),
				}
				foundInterfaces := make(map[string]bool)
				for _, result := range results {
					if result.Metric.Namespace != consts.TestingNamespace ||
						result.Metric.Pod != "metricpod" ||
						result.Value[1] != "0" ||
						foundInterfaces[result.Metric.Interface] {
						return false
					}

					switch result.Metric.Interface {
					case "eth0":
						if result.Metric.NetworkName == "" {
							return false
						}
					case "net1", "net2":
						if result.Metric.NetworkName != expectedNetworks[result.Metric.Interface] {
							return false
						}
					default:
						return false
					}

					foundInterfaces[result.Metric.Interface] = true
				}

				return foundInterfaces["eth0"] && foundInterfaces["net1"] && foundInterfaces["net2"]
			})
		})
	})
})

func queryPrometheusEventually(query string, total time.Duration, interval time.Duration, validate func(queryOutput prometheus.Reply) bool) {
	var queryOutput prometheus.Reply
	gomega.Eventually(func() error {
		jsonReply, err := prometheus.Query(query)
		if err != nil {
			return fmt.Errorf("query failed: %w", err)
		}

		response := jsonReply.String()
		err = json.Unmarshal([]byte(response), &queryOutput)
		if err != nil {
			return fmt.Errorf("could not decode Prometheus response %q: %w", response, err)
		}
		if queryOutput.Status != "success" {
			return fmt.Errorf("Prometheus query returned status %q: %s", queryOutput.Status, response)
		}
		if len(queryOutput.Data.Results) <= 0 {
			return fmt.Errorf("Prometheus query returned no results: %s", response)
		}
		if !validate(queryOutput) {
			return fmt.Errorf("Prometheus response did not pass validation: %s", response)
		}

		return nil
	}, total, interval).Should(gomega.Succeed(), "Prometheus query: %s", query)
}
