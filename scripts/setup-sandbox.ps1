$ErrorActionPreference = 'Stop'
foreach ($runtime in @(@{Base='gcc:14-bookworm'; File='infra/sandbox/Dockerfile.cpp'; Tag='judgegrid-sandbox-cpp:session4'; Key='cpp17'}, @{Base='python:3.13-slim-bookworm'; File='infra/sandbox/Dockerfile.python'; Tag='judgegrid-sandbox-python:session4'; Key='python3'})) {
    docker pull $runtime.Base
    if ($LASTEXITCODE -ne 0) { throw 'Runtime prefetch failed' }
    $digest = docker image inspect --format '{{index .RepoDigests 0}}' $runtime.Base
    docker build --build-arg "BASE_IMAGE=$digest" -f $runtime.File -t $runtime.Tag infra/sandbox
    if ($LASTEXITCODE -ne 0) { throw 'Trusted runtime build failed' }
    $runtime.Identity = docker image inspect --format '{{.Id}}' $runtime.Tag
    if ($runtime.Key -eq 'cpp17') { $cpp = $runtime.Identity; $cppBase = $digest } else { $python = $runtime.Identity; $pythonBase = $digest }
}
@{cpp17=$cpp; python3=$python; cpp_base=$cppBase; python_base=$pythonBase} | ConvertTo-Json | Set-Content -Encoding ascii .sandbox-images.local.json
Write-Output 'Pinned local image identities saved. No untrusted workload was executed during setup.'
