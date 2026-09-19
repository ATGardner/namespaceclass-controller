/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package common

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var _ = Describe("templateResourceNamespace", func() {
	It("replaces {{ .namespace }} in a data value", func() {
		u := toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm
data:
  foo: "{{ .namespace }}"
`)
		res, err := templateResourceNamespace(&u, "web-portal")
		Expect(err).NotTo(HaveOccurred())

		val, found, err := unstructured.NestedString(res.Object, "data", "foo")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(val).To(Equal("web-portal"))
	})

	It("replaces the placeholder wherever it appears, not just in data", func() {
		u := toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: "cm-{{ .namespace }}"
  labels:
    owner: "{{ .namespace }}"
`)
		res, err := templateResourceNamespace(&u, "web-portal")
		Expect(err).NotTo(HaveOccurred())

		Expect(res.GetName()).To(Equal("cm-web-portal"))
		Expect(res.GetLabels()).To(HaveKeyWithValue("owner", "web-portal"))
	})

	It("replaces every occurrence with the same value", func() {
		u := toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: "{{ .namespace }}"
data:
  a: "{{ .namespace }}"
  b: "{{ .namespace }}"
`)
		res, err := templateResourceNamespace(&u, "web-portal")
		Expect(err).NotTo(HaveOccurred())

		Expect(res.GetName()).To(Equal("web-portal"))
		a, _, _ := unstructured.NestedString(res.Object, "data", "a")
		b, _, _ := unstructured.NestedString(res.Object, "data", "b")
		Expect(a).To(Equal("web-portal"))
		Expect(b).To(Equal("web-portal"))
	})

	It("leaves a resource with no placeholder unchanged", func() {
		u := toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: plain-cm
data:
  foo: bar
`)
		res, err := templateResourceNamespace(&u, "web-portal")
		Expect(err).NotTo(HaveOccurred())

		Expect(res.GetName()).To(Equal("plain-cm"))
		val, _, _ := unstructured.NestedString(res.Object, "data", "foo")
		Expect(val).To(Equal("bar"))
	})

	It("fails on an unknown placeholder instead of silently applying <no value>", func() {
		u := toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm
data:
  foo: "{{ .banana }}"
`)
		_, err := templateResourceNamespace(&u, "web-portal")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("banana"))
	})

	It("fails cleanly on malformed template syntax", func() {
		u := toUnstructured(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm
data:
  foo: "{{ .namespace"
`)
		_, err := templateResourceNamespace(&u, "web-portal")
		Expect(err).To(HaveOccurred())
	})
})

func toUnstructured(data string) unstructured.Unstructured {
	var res unstructured.Unstructured
	Expect(yaml.Unmarshal([]byte(data), &res)).To(Succeed())
	return res
}
