#!/bin/bash

echo "Installing docker ..."

( sudo apt-get update -y ) > dependency-install.out

( curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg ) >> dependency-install.out

( echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu \
  $(lsb_release -cs) stable" | sudo tee /etc/apt/sources.list.d/docker.list ) > /dev/null

( sudo apt-get update -y; \
  sudo apt-get install -y docker-ce docker-ce-cli containerd.io; \
  sleep 5 ) >> dependency-install.out

echo "Running image ..."

CMD="chmod +x /test-files/${TEST_SCRIPT}; /test-files/${TEST_SCRIPT}"

sudo docker run -d -v `pwd`/test-files:/test-files ${REGISTRY_URL} sh -c "$CMD"
