#!/bin/bash
sudo apt-get update -y

curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg

echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu \
  $(lsb_release -cs) stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null

sudo apt-get update -y && sudo apt-get install -y docker-ce docker-ce-cli containerd.io && \
 		sleep 5 && \
		sudo docker login -u ${DOCKER_LOGIN} --password-stdin ${DOCKER_PASSWORD} && \
		sudo docker run -p 2002:2001 -p 41504:41504 -v `pwd`/test-files:/test-files ${REGISTRY_URL} &
