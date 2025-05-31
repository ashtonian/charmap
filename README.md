# charmap

Simple utility to replace text patterns from flags or env variables in all files within a directory. Slightly optimized, faster then sed.

charmap recursively scans every regular file beneath the chosen directory and replaces occurrences of tokens like <::KEY::> using values supplied from environment variables, command-line flags, or both. Delimiters, file filters, and the source of key values are all configurable through flags, and processing is performed concurrently for speed. Optional logging to a file is also supported.

## Why

Better performance, easier to maintain and configure then sed.

## Usage

```yaml
charmap \
  -open '{{' \
  -close '}}' \
  -dir ./manifests \
  -workers 8 \
  -mode both \
  -log /tmp/charmap.log \
  -include '.*\.ya?ml$' \
  -include '.*\.json$' \
  -ignore '^vendor/' \
  -ignore '^\.git(/|$)' \
  -set PUBLIC_DOMAIN=example.com,API_KEY=deadbeef \
  -set VERSION=1.2.3


docker run --rm -v "$PWD":/work -w /work \
    -e KEY_TO_REPLACE=somevalue \
    charmap -mode both \
            -set ANOTHER_KEY_TO_REPLACE=another_value \
            -log /work/charmap.log
```

argocd-lovely-plugin preprocessor, setup via argocd helm chart:

```yaml
repoServer:
  volumes:
    - name: charmap-bin
      emptyDir: {}
  initContainers:
    - name: install-charmap
      image: TODO:
      imagePullPolicy: IfNotPresent
      command:
        - /bin/sh
        - -c
        - |
          cp /usr/local/bin/charmap /shared/ \
            && chmod 0755 /shared/charmap
      volumeMounts:
        - name: charmap-bin
          mountPath: /shared

  extraContainers:
    - name: argocd-lovely-plugin
      image: ghcr.io/crumbhole/argocd-lovely-plugin:latest
      imagePullPolicy: IfNotPresent
      env:
        - name: SOME_KEY
          value: somevalue
        - name: LOVELY_PREPROCESSORS
          value: >-
            charmap
      volumeMounts:
        - name: charmap-bin
          mountPath: /usr/local/bin/charmap
          subPath: charmap
```