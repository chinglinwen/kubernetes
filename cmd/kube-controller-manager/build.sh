go build
tar -czf kube-controller-manager-wen.tar.gz kube-controller-manager
curl -s fs.qianbao-inc.com/k8s/soft/uploadapi -F file=@kube-controller-manager-wen.tar.gz -F truncate=yes
cksum kube-controller-manager
