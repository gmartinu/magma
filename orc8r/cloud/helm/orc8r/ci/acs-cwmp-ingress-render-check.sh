#!/usr/bin/env bash
# Renders the orc8r chart in the acs/CWMP-ingress configurations and asserts
# which resources appear. Usage: ci/acs-cwmp-ingress-render-check.sh [helm]
set -euo pipefail

HELM="${1:-helm}"
CHART="$(cd "$(dirname "$0")/.." && pwd)"
BASE=(--set nginx.spec.hostname=orc8r.test)
ING=(--set acs.enabled=true --set acs.cwmp.ingress.enabled=true --set acs.cwmp.ingress.hostname=cwmp.test)
CM=(--set acs.cwmp.ingress.tls.certManager.issuerRef.name=letsencrypt)
SECRET=(--set acs.cwmp.ingress.tls.mode=existingSecret --set acs.cwmp.ingress.tls.existingSecret.name=cwmp-tls)

"$HELM" dependency build "$CHART" >/dev/null 2>&1 || true
"$HELM" lint "$CHART" "${BASE[@]}" "${ING[@]}" "${CM[@]}" >/dev/null

render() { "$HELM" template t "$CHART" "${BASE[@]}" "$@"; }
fail() { echo "FAIL: $1" >&2; exit 1; }
has()  { grep -q -- "$2" <<<"$1" || fail "$3: expected '$2'"; }
hasnt() { ! grep -q -- "$2" <<<"$1" || fail "$3: unexpected '$2'"; }

out=$(render)
hasnt "$out" 'orc8r-acs' "acs off"
hasnt "$out" 'acs-cwmp' "acs off"

out=$(render --set acs.enabled=true)
has "$out" 'name: orc8r-acs' "acs on, ingress off"
hasnt "$out" 'acs-cwmp' "acs on, ingress off"
hasnt "$out" 'kind: NetworkPolicy' "acs on, ingress off"
has "$out" 'name: orc8r-acs-secrets' "generated secret"
has "$out" 'helm.sh/resource-policy: keep' "generated secret"
has "$out" 'inform_rate_limit: 30' "rate limit passed"
has "$out" 'inform_rate_window_sec: 300' "rate limit passed"
hasnt "$out" 'trust_proxy_headers' "ingress off"
hasnt "$out" 'bootstrap_password' "no plaintext password"
envfrom=$(grep -A2 'envFrom:' <<<"$out")
has "$envfrom" 'name: orc8r-acs-secrets' "acs env from secret"
key=$(sed -n 's/^ *ACS_ENCRYPTION_KEY: //p' <<<"$out" | base64 -d | base64 -d | wc -c | tr -d ' ')
[ "$key" = 32 ] || fail "generated encryption key must decode to 32 bytes, got $key"
user=$(sed -n 's/^ *ACS_BOOTSTRAP_USERNAME: //p' <<<"$out" | base64 -d)
[ "$user" = acs-bootstrap ] || fail "generated bootstrap username, got '$user'"

out=$(render --set acs.enabled=true --set acs.secrets.existingSecret=my-acs)
hasnt "$out" 'orc8r-acs-secrets' "existing acs secret"
envfrom=$(grep -A2 'envFrom:' <<<"$out")
has "$envfrom" 'name: my-acs' "existing acs secret"

render --set acs.enabled=true --set acs.config.bootstrap_password=x >/dev/null 2>&1 && fail "plaintext bootstrap password must fail"

out=$(render "${ING[@]}" "${CM[@]}")
has "$out" 'trust_proxy_headers: true' "ingress on"
has "$out" 'kind: Certificate' "cert-manager"
has "$out" 'secretName: t-acs-cwmp-tls' "cert-manager"
has "$out" 'X-Forwarded-Proto \$scheme' "cert-manager"
has "$out" 'kind: NetworkPolicy' "cert-manager"
has "$out" 'type: LoadBalancer' "cert-manager"
hasnt "$out" 'cwmp-plain' "plain http off by default"

out=$(render "${ING[@]}" "${SECRET[@]}")
hasnt "$out" 'kind: Certificate' "existing secret"
has "$out" 'secretName: cwmp-tls' "existing secret"

out=$(render "${ING[@]}" "${CM[@]}" --set acs.cwmp.ingress.plainHttp.enabled=true)
has "$out" 'name: cwmp-plain' "plain http on"

render "${ING[@]}" >/dev/null 2>&1 && fail "cert-manager without issuer must fail"
render --set acs.enabled=true --set acs.cwmp.ingress.enabled=true >/dev/null 2>&1 && fail "missing hostname must fail"
echo "acs cwmp ingress render checks passed"
