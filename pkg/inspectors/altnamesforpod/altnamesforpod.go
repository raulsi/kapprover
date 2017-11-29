package altnamesforpod

import (
	"fmt"
	"github.com/proofpoint/kapprover/pkg/csr"
	"github.com/proofpoint/kapprover/pkg/inspectors"
	"github.com/proofpoint/kapprover/pkg/podnames"
	certificates "k8s.io/api/certificates/v1beta1"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"strings"
	"encoding/asn1"
	"net"
)

func init() {
	inspectors.Register("altnamesforpod", &altnamesforpod{"cluster.local"})
}

// AltNamesForPod is an Inspector that verifies all the Subject Alt Names in the CSR are appropriate
// for the POD named in the subject
type altnamesforpod struct {
	clusterDomain string
}

var (
	oidExtensionSubjectAltName        = []int{2, 5, 29, 17}
)

func (a *altnamesforpod) Configure(config string) error {
	if config != "" {
		a.clusterDomain = config
	}
	return nil
}

func (a *altnamesforpod) Inspect(client kubernetes.Interface, request *certificates.CertificateSigningRequest) (string, error) {
	certificateRequest, msg := csr.Extract(request.Spec.Request)
	if msg != "" {
		return msg, nil
	}

	podIp, namespace, msg := csr.GetPodIpAndNamespace(a.clusterDomain, certificateRequest)
	if msg != "" {
		return msg, nil
	}

	podList, err := client.CoreV1().Pods(namespace).List(metaV1.ListOptions{FieldSelector: "status.podIp=" + podIp})
	if err != nil {
		return "", err
	}
	if len(podList.Items) == 0 {
		return fmt.Sprintf("No POD in namespace %q with IP %q", namespace, podIp), nil
	}

	permittedDnsnames, permittedIps, err := podnames.GetNamesForPod(client, podList.Items[0], a.clusterDomain)
	if err != nil {
		return "", err
	}

	var badNames []string

	for _, extension := range certificateRequest.Extensions {
		if !extension.Id.Equal(oidExtensionSubjectAltName) {
			continue
		}
		var seq asn1.RawValue
		var rest []byte
		if rest, err = asn1.Unmarshal(extension.Value, &seq); err != nil {
			return fmt.Sprintf("Could not parse SubjectAltName: %v", err), nil
		} else if len(rest) != 0 {
			return "Trailing data after X.509 SubjectAltName extension", nil
		}
		if !seq.IsCompound || seq.Tag != 16 || seq.Class != 0 {
			return "Bad SubjectAltName sequence", nil
		}

		rest = seq.Bytes
		for len(rest) > 0 {
			var v asn1.RawValue
			rest, err = asn1.Unmarshal(rest, &v)
			if err != nil {
				return fmt.Sprintf("Could not parse SubjectAltName: %v", err), nil
			}
			switch v.Tag {
			case 2:
				dnsName := string(v.Bytes)
				found := false
				for _, permittedDnsname := range permittedDnsnames {
					if dnsName == permittedDnsname {
						found = true
						break
					}
				}
				if !found {
					badNames = append(badNames, dnsName)
				}
			case 7:
				ip := net.IP(v.Bytes)
				found := false
				for _, permittedIp := range permittedIps {
					if ip.Equal(permittedIp) {
						found = true
						break
					}
				}
				if !found {
					badNames = append(badNames, ip.String())
				}
			default:
				badNames = append(badNames, fmt.Sprintf("Name of type %v", v.Tag))
			}
		}


	}

	if len(badNames) != 0 {
		msg = "Subject Alt Name contains disallowed name"
		if len(badNames) != 1 {
			msg += "s"
		}
		msg += ": "
		msg += strings.Join(badNames, ",")
		return msg, nil
	}

	return "", nil
}
