Is is possible to build an a local proxy which intercepts and caches apt/apt-get installation dependencies and docker image pulls? The idea is a local cache which makes apt installs and docker pulls fast even if the docker images are purged. Let's discuss the mechanisms first. You can suggest existing proxy solutions (must be open-sourced).

---

Yes. This is quite practical, and for your SWE-bench-style evaluator it could remove a lot of repeated network I/O.

The key is not to build one generic HTTP cache and hope it understands everything. APT repositories and OCI/Docker registries have different semantics, so I would treat this as **one local caching service with two protocol-aware frontends**.

Conceptually:

```text
                 ┌──────────────────────────┐
apt/apt-get ────▶│ APT caching proxy        │──▶ Ubuntu/Debian mirrors
                 │ persistent cache volume  │
                 └──────────────────────────┘

                 ┌──────────────────────────┐
docker pull ────▶│ OCI registry mirror      │──▶ Docker Hub / GHCR / etc.
                 │ persistent blob storage  │
                 └──────────────────────────┘

                         │
                         ▼
                  /var/cache/eval/
                  survives Docker prune
```

That distinction matters because `docker image prune`, deleting containers, or even blowing away `/var/lib/docker` would not affect either cache.

### APT is the easier half

APT downloads two broad classes of objects:

```text
repository metadata:
  InRelease
  Release
  Packages.gz
  Packages.xz
  Sources.gz
  ...

immutable-ish package payloads:
  *.deb
```

The `.deb` files are especially cache-friendly. Once you have:

```text
libssl3_3.0.x_amd64.deb
```

the contents corresponding to that repository path don't normally change. New versions get new filenames.

A caching proxy therefore works roughly like:

```text
apt GET /ubuntu/pool/main/f/foo/foo_1.2.3_amd64.deb
                │
                ▼
        cache lookup by URL
          │            │
        HIT           MISS
          │            │
          │       fetch upstream
          │       save response
          │            │
          └──────▶ return .deb
```

Repository indexes require normal HTTP freshness/revalidation rules, while package payloads can effectively be retained for a long time.

The obvious existing open-source tool is **Apt-Cacher NG**. Debian describes it specifically as a caching proxy for package repositories: clients send APT downloads through it and it stores useful responses so subsequent downloads don't hit the remote mirror. ([Debian Packages][1])

For an evaluator, that gives you something like:

```text
host:
    apt-cacher-ng :3142
         │
         └── persistent /cache/apt

evaluation container:
    Acquire::http::Proxy "http://host:3142";
```

You don't need to modify every `RUN apt-get install ...` command.

There is one complication: **HTTPS**.

With ordinary HTTPS proxying:

```text
APT → proxy → CONNECT archive.example.com:443
                  │
                  └── encrypted TLS tunnel
```

the proxy cannot see the HTTP objects inside the TLS session, so a generic CONNECT proxy cannot meaningfully cache the `.deb` responses. This is why protocol-aware approaches, repository remapping, or using HTTP Debian/Ubuntu mirrors are relevant. Traditional apt-cacher documentation explicitly distinguishes cacheable HTTP/HTTPS GET behavior from HTTPS `CONNECT`, which is proxied rather than cached. ([Debian Manpages][2])

For Debian/Ubuntu base images this is often manageable because you control the build environment and can deliberately configure sources for the cache rather than trying to transparently MITM TLS.

I would **not** install a local CA and intercept HTTPS unless there were a compelling reason. It adds certificate management and creates surprising behavior inside evaluator containers.

---

### Docker pulls need a registry, not a conventional HTTP proxy

OCI images are already almost ideal for caching because layers are content-addressed:

```text
ubuntu:24.04
     │
     ▼
manifest
 ├── sha256:AAA  layer
 ├── sha256:BBB  layer
 └── sha256:CCC  layer
```

If another image contains layer `sha256:BBB`, the same blob can be reused.

A registry pull-through cache works as:

```text
dockerd
   │
   ▼
local registry mirror
   │
   ├── manifest cached? ── yes ──▶ serve
   │
   └── missing blob?
          │
          ▼
      upstream registry
          │
          ▼
     store sha256 blob
          │
          ▼
        serve
```

So after:

```bash
docker system prune -a
```

Docker's own copy disappears:

```text
/var/lib/docker/
```

but the mirror still has:

```text
/cache/registry/
    blobs/sha256/...
```

The next `docker pull ubuntu:24.04` transfers the layers over localhost/disk instead of the internet.

The CNCF **Distribution Registry**—the open-source registry formerly commonly called Docker Registry v2—supports exactly this pull-through-cache mode. On the first request it fetches and stores content; subsequent pulls are served from local storage. ([GitHub Distribution][3])

Docker can be configured with:

```json
{
  "registry-mirrors": ["https://registry-cache.local"]
}
```

and ordinary commands remain unchanged:

```bash
docker pull ubuntu:24.04
docker pull node:24
```

Docker documents this same mechanism for a Docker Hub mirror. ([Docker Documentation][4])

There is an important limitation, though: the straightforward Docker daemon `registry-mirrors` mechanism is primarily for **Docker Hub**, and Distribution itself supports only one upstream per pull-through-cache instance. ([GitHub Distribution][3])

So if your builds use:

```text
docker.io
ghcr.io
quay.io
registry.k8s.io
mcr.microsoft.com
```

a single vanilla Distribution instance isn't the whole solution.

---

### Open-source choices I'd consider

For APT, **Apt-Cacher NG** is probably the default choice. It's mature, very lightweight, built specifically for this problem, and available directly in Debian. ([Debian Packages][1])

For OCI images there are three especially interesting options.

**CNCF Distribution Registry** is the simplest. It is excellent if almost everything comes from Docker Hub. Its pull-through behavior is very straightforward, but one cache instance maps to one upstream. ([GitHub Distribution][3])

**zot** is much more interesting for the generalized evaluator you're describing. It is an open-source OCI registry designed by the CNCF ecosystem and supports on-demand mirroring of multiple upstream OCI registries. Its documentation specifically describes the pull-through-cache use case: a requested image is fetched on first access and then kept in local storage. ([Zot Registry][5])

Recent zot versions also document using it with Docker Hub and other registries, including Docker/containerd host configuration for things like:

```text
docker.io
gcr.io
ghcr.io
mcr.microsoft.com
public.ecr.aws
quay.io
registry.gitlab.com
registry.k8s.io
```

([Zot Registry][6])

That makes zot particularly attractive for SWE-bench because candidate repositories may reference all sorts of base images.

One subtle zot setting matters for reproducibility. Docker-format images can otherwise be converted to OCI representation, changing the manifest digest. For digest-pinned images, zot recommends enabling Docker schema compatibility plus `preserveDigest: true`. ([Zot Registry][5])

**Harbor** is the heavyweight alternative. Its proxy-cache projects can sit in front of Docker Hub, Harbor, Quay, GHCR, AWS ECR, Azure, GCR and others. ([goharbor.io][7])

Harbor gives you much more:

```text
RBAC
web UI
projects
retention
scanning
registry management
proxy caches
```

but for a single development workstation or benchmark runner I think it is probably substantially more machinery than necessary.

So, at this stage, my shortlist would be:

| Purpose                           | Candidate             | My reason                       |
| --------------------------------- | --------------------- | ------------------------------- |
| APT                               | **apt-cacher-ng**     | Purpose-built and tiny          |
| OCI, simple Docker Hub            | **CNCF Distribution** | Very simple                     |
| OCI, arbitrary registries         | **zot**               | Multi-registry/on-demand design |
| OCI, larger shared infrastructure | **Harbor**            | Full registry platform          |

---

There is also an important distinction between **proxying** and **interception**.

I would favor explicit configuration:

```text
APT
  Acquire::http::Proxy → apt-cache

Docker/containerd
  registry mirror → OCI cache
```

rather than attempting:

```text
iptables redirect
        +
transparent TLS interception
        +
generic HTTP cache
```

The explicit approach is both safer and more correct. Registry authentication, token exchanges, manifests, mutable tags, HTTP ranges, digest validation, and TLS make transparent generic interception surprisingly complicated.

For your evaluator, we control the Docker daemon and usually control how the evaluation container is launched, so there is little benefit to true transparent interception.

---

There is one particularly nice consequence for the architecture we were discussing earlier.

You could end up with **three independent cache layers**:

```text
┌─────────────────────────────────────────────┐
│ SWE-bench evaluator                         │
│                                             │
│  1. Prebuilt evaluator/base image           │
│     avoids repeated dependency installation │
│                                             │
│  2. OCI pull-through cache                  │
│     makes missing images cheap to restore   │
│                                             │
│  3. apt-cacher-ng                           │
│     makes apt installs cheap even on a      │
│     Docker cache miss                       │
└─────────────────────────────────────────────┘
```

These solve different problems.

For example, suppose you completely delete:

```text
all evaluator containers
all candidate containers
all Docker images
all Docker build cache
```

but retain:

```text
/srv/eval-cache/apt/
/srv/eval-cache/oci/
```

Rebuilding would still be much faster:

```text
docker pull node:24
       │
       └── layers retrieved from local OCI cache

docker build ...
    RUN apt-get update && apt-get install ...
       │
       └── .debs retrieved from local APT cache
```

That gives you the property you're after: **Docker state becomes disposable while downloaded internet artifacts remain reusable**.

I think that is a better abstraction than trying to preserve `/var/lib/docker` indefinitely.

For our particular lab setup, I'd lean toward **apt-cacher-ng + zot**, each with a bind-mounted persistent cache directory outside Docker's own storage. Then Docker/containerd and evaluation containers would be configured to discover those caches automatically, so the candidate/evaluator scripts don't need to know they're present.

[1]: https://packages.debian.org/stable/apt-cacher-ng?utm_source=chatgpt.com "Debian -- Details of package apt-cacher-ng in trixie"
[2]: https://manpages.debian.org/unstable/apt-cacher/apt-cacher.8.en.html?utm_source=chatgpt.com "apt-cacher(8) — apt-cacher — Debian unstable — Debian Manpages"
[3]: https://distribution.github.io/distribution/recipes/mirror/?utm_source=chatgpt.com "Registry as a pull through cache | CNCF Distribution"
[4]: https://docs.docker.com/docker-hub/image-library/mirror/?utm_source=chatgpt.com "Mirror the Docker Hub library | Docker Docs"
[5]: https://zotregistry.dev/v2.1.20/articles/mirroring/?utm_source=chatgpt.com "Mirroring - zotregistry.dev"
[6]: https://zotregistry.dev/v2.1.18/articles/docker/?utm_source=chatgpt.com "Using Docker with zot - zotregistry.dev"
[7]: https://goharbor.io/docs/edge/administration/configure-proxy-cache/?utm_source=chatgpt.com "Harbor docs | Configure Proxy Cache"

---

GitHub clones also take a significant amount of time. Let's include GitHub clones in the caching discussion.

---

Yes. Git clones belong in the same local-cache architecture, but I would cache them at the **Git object/repository level**, not as ordinary HTTP responses.

The resulting model becomes:

```text
                     ┌──────────────────────┐
apt/apt-get ────────▶│ APT cache            │──▶ Debian/Ubuntu mirrors
                     │ apt-cacher-ng        │
                     └──────────────────────┘

                     ┌──────────────────────┐
docker pull ────────▶│ OCI registry cache   │──▶ Docker Hub/GHCR/...
                     │ zot / Distribution   │
                     └──────────────────────┘

                     ┌──────────────────────┐
git clone/fetch ────▶│ Git repository cache │──▶ GitHub
                     │ bare mirrors         │
                     └──────────────────────┘

All persistent cache data lives outside /var/lib/docker.
```

### Why a generic HTTP proxy is a poor Git cache

A typical HTTPS clone such as:

```bash
git clone https://github.com/pixijs/pixijs.git
```

isn't simply downloading a fixed tarball. Git negotiates with the server about which refs and objects the client already has, and the server generates packfiles for the requested object set. In other words, repeated clones don't necessarily map to one stable cacheable HTTP object.

That's why Git hosting systems themselves optimize `git-upload-pack` / `git pack-objects`, rather than relying on a normal HTTP caching proxy. GitLab's Gitaly, for example, has a `pack-objects` cache specifically for repeated clone/fetch traffic, though its cache is aimed more at repeated identical requests and isn't what I'd deploy merely to cache GitHub. ([GitLab Docs][1])

For our use case, a local **bare mirror** is much more useful.

### Bare-mirror caching

Suppose the evaluator needs:

```text
https://github.com/pixijs/pixijs.git
```

The first time:

```text
GitHub
  │
  │ network clone
  ▼
/srv/eval-cache/git/
    github.com/
      pixijs/
        pixijs.git/
```

That repository is created with approximately:

```bash
git clone --mirror \
  https://github.com/pixijs/pixijs.git \
  /srv/eval-cache/git/github.com/pixijs/pixijs.git
```

`--mirror` implies a bare repository and mirrors all refs, with configuration suitable for subsequent remote updates. ([Git][2])

Then an evaluator needing a fresh checkout can clone from the local mirror:

```text
local bare mirror
      │
      │ local filesystem clone
      ▼
/tmp/eval-123/pixijs
```

A local Git clone has an additional advantage: Git can hardlink repository objects instead of copying them where possible. Git explicitly documents this optimization for local-path clones. ([Git][2])

So a 500 MB repository does not necessarily mean another 500 MB gets copied every evaluation.

For SWE-bench this is particularly attractive because we normally need:

```text
repository + known base commit + patch
```

rather than necessarily needing a newly generated server-side shallow pack on every run.

### Updating the cache

Before using a mirror, the cache can optionally do:

```bash
git -C /srv/eval-cache/git/github.com/pixijs/pixijs.git \
    remote update --prune
```

Then:

```text
First evaluation:
GitHub ───── 700 MB ─────▶ mirror

Later GitHub changes:
GitHub ───── 3 MB ───────▶ mirror

Every evaluator:
mirror ─── local disk ───▶ working clone
```

That is substantially better than each evaluator starting from zero.

More importantly, SWE-bench instances usually refer to historical commits. Once those objects have been obtained, they are effectively immutable. So many evaluations may require **zero GitHub traffic**.

### There is already an open-source implementation very close to this

`seeraven/gitcache` is specifically designed around this model. It is an open-source wrapper around Git that transparently creates and maintains local bare mirrors and rewrites clones to use them. Its documented example turns:

```bash
git clone https://github.com/seeraven/gitcache.git
```

into a clone backed by a cached mirror under a hierarchy corresponding to the remote URL. It also supports Git LFS, configurable mirror refresh intervals, timeouts and statistics. ([GitHub][3])

Architecturally, that is almost exactly what we're discussing.

I would definitely study its implementation before building our own.

### We can make the cache effectively transparent

There are several levels of transparency.

At the simplest level the evaluator knows about the cache:

```bash
eval-clone https://github.com/pixijs/pixijs.git /workspace/repo
```

where `eval-clone` does:

```text
1. derive cache key from remote URL
2. create mirror if absent
3. fetch/update mirror if appropriate
4. clone locally
5. set origin back to the real GitHub URL
6. checkout requested commit
```

That's easy and robust.

But Git itself gives us another interesting mechanism: URL rewriting with `url.<base>.insteadOf`.

Conceptually we can transform:

```text
https://github.com/foo/bar.git
```

into:

```text
http://git-cache.local/github.com/foo/bar.git
```

without changing the repository's build scripts.

The local service would then behave like a Git server backed by mirrors.

This would let ordinary commands remain:

```bash
git clone https://github.com/foo/bar.git
```

while the environment's Git configuration redirects them.

However, for our evaluator I suspect there's an even simpler route.

### Don't proxy `git clone`; intercept the evaluator's repository acquisition

Our benchmark already controls the step that obtains the repository.

Instead of:

```bash
git clone "$REPO_URL" repo
cd repo
git checkout "$BASE_COMMIT"
```

the evaluator can implement:

```text
ensure mirror exists
        │
        ▼
ensure requested commit exists
        │
        ▼
clone from mirror
        │
        ▼
reset --hard requested commit
        │
        ▼
apply candidate patch
```

For example, conceptually:

```bash
MIRROR=/srv/eval-cache/git/github.com/pixijs/pixijs.git

if [ ! -d "$MIRROR" ]; then
    git clone --mirror \
        https://github.com/pixijs/pixijs.git \
        "$MIRROR"
fi

git clone "$MIRROR" /workspace/repo

cd /workspace/repo
git remote set-url origin \
    https://github.com/pixijs/pixijs.git

git checkout --detach "$BASE_COMMIT"
git reset --hard "$BASE_COMMIT"
git clean -fdx
```

There is an important design choice here: **do we update the mirror on every evaluation?**

For SWE-bench, probably not.

If the required commit already exists:

```bash
git -C "$MIRROR" cat-file -e "$BASE_COMMIT^{commit}"
```

there is no reason to talk to GitHub at all.

So:

```text
requested commit
      │
      ▼
exists locally? ── yes ──▶ clone immediately
      │
      no
      ▼
git fetch/update from GitHub
      │
      ▼
exists now?
```

That gives us deterministic caching behavior and minimizes network access.

### `--reference` gives us another option

Git also supports:

```bash
git clone \
  --reference-if-able /srv/eval-cache/git/.../pixijs.git \
  https://github.com/pixijs/pixijs.git repo
```

A reference repository is placed in Git's `objects/info/alternates`, allowing the new repository to reuse existing objects instead of downloading/copying them. ([Git][2])

There are two strategies here.

**Mirror → local clone**

```text
GitHub ─▶ mirror

mirror ─▶ repo A
mirror ─▶ repo B
mirror ─▶ repo C
```

This is what I'd prefer.

Or:

**GitHub clone with mirror as an alternate**

```text
                ┌── existing objects ─▶ mirror
repo clone ─────┤
                └── missing objects ──▶ GitHub
```

That's elegant for general development machines, but our evaluator has more control, so the first approach is cleaner.

### We should avoid fragile shared-object dependencies

Git has `--shared`, local hardlinks and alternates that can make clones extremely cheap, but Git's own documentation warns about object lifetime: if an object exists only in the source/reference repository and later gets garbage-collected there, dependent repositories can become corrupt. ([Git][2])

For short-lived evaluation containers, that isn't terribly scary because:

```text
cache mirror lifetime: months
evaluation clone lifetime: minutes
```

and we control garbage collection.

Still, I'd establish an invariant:

```text
Never aggressively prune unreachable objects from Git mirrors.
```

Disk is cheaper than re-downloading historical commits or breaking a running evaluator.

We could periodically repack, but avoid:

```bash
git gc --prune=now
```

on these cache repositories.

### GitHub archives are another possible optimization, but I wouldn't use them as the primary mechanism

If all we needed were source files at one commit, we could cache:

```text
https://github.com/owner/repo/archive/<sha>.tar.gz
```

That can be exceptionally fast.

But SWE-bench often benefits from having a genuine Git repository because we need things such as:

```bash
git diff
git apply
git status
git reset --hard
git checkout <commit>
```

and sometimes tests/build scripts inspect Git metadata.

So I would keep a real Git mirror.

### Git LFS needs separate consideration

If a repository uses Git LFS:

```text
Git repository:
    small pointer object

LFS server:
    actual 2 GB model/data/archive
```

A Git mirror alone does not necessarily cache the actual LFS payloads.

`gitcache` is notable here because it advertises Git LFS support. ([GitHub][3])

For a generic solution we'd therefore eventually want:

```text
Git object cache
+
Git LFS object cache
```

For SWE-bench this may not be urgent because typical software repositories don't depend heavily on LFS, but it belongs in the design.

### So now we have three different notions of identity

This is one reason I like having one overarching cache manager but protocol-specific storage underneath.

```text
APT
cache key ≈ repository URL/path/version

OCI
cache key = sha256 content digest

Git
cache key = repository + Git object SHA
```

And in all three cases the heavyweight immutable payload naturally deduplicates.

For example:

```text
/srv/swe-cache/

    apt/
        ...

    oci/
        ...

    git/
        github.com/
            pixijs/
                pixijs.git/
            django/
                django.git/
            pallets/
                flask.git/

    git-lfs/
        ...
```

Docker can be completely destroyed:

```bash
docker system prune -af --volumes
```

and none of those need disappear.

### For our evaluator specifically, I'd rank the Git approaches this way

| Approach                                 | Suitability                                                  |
| ---------------------------------------- | ------------------------------------------------------------ |
| Bare Git mirrors controlled by evaluator | **Excellent**                                                |
| `gitcache`                               | **Very interesting reference / potentially usable directly** |
| Git `--reference`                        | Useful optimization                                          |
| Git smart-HTTP proxy                     | More complexity than we need                                 |
| Generic Squid HTTP cache                 | Poor match                                                   |
| Transparent HTTPS interception           | Avoid                                                        |

`gitcache` is the closest existing open-source solution I've found to the exact Git portion of our problem: persistent local bare mirrors used automatically to accelerate repeated clones. ([GitHub][3])

The bigger architectural idea I now favor is therefore:

```text
                 Local Artifact Cache
                         │
        ┌────────────────┼────────────────┐
        │                │                │
        ▼                ▼                ▼
   Git mirrors        OCI CAS          APT cache
   Git objects        layers/blobs      .deb files
        │                │                │
        └────────────────┼────────────────┘
                         │
                         ▼
                    Evaluator
```

And there's a valuable property here: **the evaluator could become almost network-independent after warmup**. If the Git commit, base image layers and Debian packages required by an instance have all been seen before, rebuilding that instance from a completely empty Docker daemon should involve essentially local filesystem and localhost traffic only.

I think the next design question worth exploring is whether we want **three existing daemons/tools** (`gitcache` or our mirror manager + zot + apt-cacher-ng), or whether it makes sense to build a small unified evaluator-oriented cache service that orchestrates these three object stores while still delegating the actual protocols to proven implementations.

[1]: https://docs.gitlab.com/administration/gitaly/configure_gitaly/?utm_source=chatgpt.com "Configure Gitaly | GitLab Docs"
[2]: https://git-scm.com/docs/git-clone?utm_source=chatgpt.com "Git - git-clone Documentation"
[3]: https://github.com/seeraven/gitcache?utm_source=chatgpt.com "GitHub - seeraven/gitcache: Local cache for git repositories to speed up working with large repositories and multiple clones. · GitHub"

---

So, give me a summary of the best paths for how you want to implement based on the above discussions?

---

The best implementation path is a **three-cache architecture with protocol-specific tools**, all backed by persistent storage outside Docker’s own data directory.

My preferred setup is:

1. **APT:** use `apt-cacher-ng`
2. **Docker/OCI:** use `zot` as a pull-through registry cache
3. **GitHub/Git:** use local bare mirrors, either via a small evaluator wrapper or by adapting `gitcache`

The important design choice is to avoid building a generic transparent proxy. APT, OCI registries, and Git all have different caching semantics, so we should let purpose-built mechanisms handle each one.

```text
                         Evaluator
                            │
        ┌───────────────────┼───────────────────┐
        │                   │                   │
        ▼                   ▼                   ▼
  Git mirror cache      OCI registry cache    APT cache
  bare repositories     zot                   apt-cacher-ng
        │                   │                   │
        ▼                   ▼                   ▼
     GitHub            Docker Hub/GHCR       Debian/Ubuntu
```

All cache data should live somewhere like:

```text
/srv/swe-cache/
├── git/
├── oci/
└── apt/
```

and crucially **not** under:

```text
/var/lib/docker
```

That means we can freely run:

```bash
docker system prune -af --volumes
```

or even replace Docker’s entire storage directory without losing downloaded artifacts.

### Git: use evaluator-controlled bare mirrors

For GitHub clones, I would not run a conventional HTTP proxy.

Instead, maintain:

```text
/srv/swe-cache/git/github.com/pixijs/pixijs.git
/srv/swe-cache/git/github.com/django/django.git
...
```

as bare mirror repositories.

The evaluator logic becomes approximately:

```text
Need repository + base commit
          │
          ▼
Does mirror exist?
      │         │
     no        yes
      │         │
clone --mirror │
      └────┬────┘
           ▼
Does requested commit exist locally?
      │                  │
     yes                 no
      │                  │
      │             fetch GitHub
      │                  │
      └──────────┬───────┘
                 ▼
        local clone/worktree
                 │
                 ▼
        checkout base commit
                 │
                 ▼
          apply SWE patch
```

The particularly useful optimization is:

```bash
git cat-file -e "$BASE_COMMIT^{commit}"
```

If that succeeds, **do not contact GitHub at all**.

For SWE-bench, that should be extremely effective because the same historical repositories and commits are repeatedly evaluated.

I would probably implement this Git piece ourselves because the core logic is small and gives us precise control over historical commits. `seeraven/gitcache` is still worth studying and could potentially be used directly, especially if we eventually need Git LFS support.

### OCI/Docker: use zot

For Docker images, I favor **zot** over plain Distribution Registry.

Plain Distribution is excellent for a Docker Hub-only mirror, but our evaluator may encounter:

```text
docker.io
ghcr.io
quay.io
registry.k8s.io
mcr.microsoft.com
public.ecr.aws
...
```

zot is better suited to multiple upstream registries and pull-through caching.

The flow becomes:

```text
docker pull node:22
        │
        ▼
      zot
        │
   layer cached?
    │       │
   yes      no
    │       │
    │     upstream
    │       │
    └─── cache blob
        │
        ▼
      dockerd
```

Because OCI blobs are content-addressed by digest, this is almost the ideal cache format.

Even if Docker deletes:

```text
node:22
ubuntu:24.04
postgres:17
```

the blobs remain in zot.

The next pull is effectively LAN/disk speed.

### APT: use apt-cacher-ng

For package installation, `apt-cacher-ng` remains the obvious choice.

Evaluation environments get something equivalent to:

```text
Acquire::http::Proxy "http://apt-cache:3142";
```

Then:

```bash
apt-get update
apt-get install ...
```

continues working normally.

The first installation downloads packages from Ubuntu/Debian; subsequent installs retrieve the `.deb` files locally.

I would avoid TLS interception. Since we control evaluator/container configuration, explicit proxy configuration is much cleaner.

### Put a thin orchestration layer above all three

I would **not** initially build our own APT proxy, OCI registry, or Git server.

Instead, build a very small `swe-cache` management layer around them.

Conceptually:

```text
swe-cache
│
├── git
│   ├── ensure-repo URL
│   ├── ensure-commit URL SHA
│   └── checkout URL SHA destination
│
├── oci
│   └── configure zot
│
├── apt
│   └── configure apt-cacher-ng
│
├── stats
│   ├── hit/miss
│   ├── bytes downloaded
│   └── disk usage
│
└── gc
    └── cache retention policy
```

The orchestration layer should manage configuration and lifecycle, not reimplement protocols.

That gives us an architecture like:

```text
                  swe-cache manager
                         │
             ┌───────────┼───────────┐
             │           │           │
             ▼           ▼           ▼
       Git mirrors      zot      apt-cacher-ng
             │           │           │
             └───────────┼───────────┘
                         │
                  /srv/swe-cache
```

### I would also keep Docker build caching separate

There are really **four** cache layers eventually:

```text
1. Git source cache
2. OCI image/layer cache
3. APT package cache
4. Docker/BuildKit build cache
```

The first three should survive Docker destruction.

BuildKit cache is more disposable and can be rebuilt from those lower-level caches.

That distinction is useful because it means we aren't relying on Docker's internal state for long-term performance.

### Recommended implementation order

I would implement them in this order:

1. **Git bare-mirror cache**

   Probably the easiest integration and likely a large win for repeated SWE-bench runs.

2. **APT caching with apt-cacher-ng**

   Very little custom engineering and immediately accelerates repeated dependency installs.

3. **zot OCI pull-through cache**

   Slightly more configuration work because we need to decide how multiple registries are mapped.

4. **Unified cache manager / metrics**

   Only after all three mechanisms work independently.

So the eventual evaluator flow would look roughly like:

```text
                   SWE instance
                        │
                        ▼
             Ensure source commit
                        │
                  Git mirror cache
                        │
                        ▼
               Create workspace
                        │
                        ▼
              Ensure base images
                        │
                   zot cache
                        │
                        ▼
                 Docker build
                        │
                  apt installs
                        │
                apt-cacher-ng
                        │
                        ▼
                    run tests
```

The main principle I'd keep is:

> **Docker state is disposable; downloaded artifacts are persistent.**

That means deleting images, containers, builders, build cache, or even `/var/lib/docker` should hurt only local reconstruction time—not force us to redownload gigabytes from GitHub, Docker registries, or Debian/Ubuntu mirrors.

---

Split the implementation to tasks but do not implement yet.

---

Yes. I’d split it into implementation tasks with clear boundaries so each layer can be built and tested independently.

1. **Define cache architecture and filesystem layout**
   - Decide canonical root, e.g. `/srv/swe-cache`.
   - Define subpaths for `git/`, `oci/`, `apt/`, logs, metrics, and config.
   - Define ownership/permissions and whether services run as host processes or containers.
   - Define the invariant that cache storage is outside `/var/lib/docker`.
   - Document which state is persistent versus disposable.
   - Deliverable: architecture/config spec only.

2. **Implement Git repository cache**
   - Define normalized cache keys from repository URLs.
   - Create bare mirrors with `git clone --mirror`.
   - Add `ensure-repo` behavior.
   - Add `ensure-commit` behavior using `git cat-file -e`.
   - Only fetch upstream when the requested commit is missing.
   - Support creating evaluator workspaces from the local mirror.
   - Ensure resulting workspaces still have the original GitHub URL as `origin` where appropriate.
   - Decide whether to use local hardlinks, alternates, or independent clones.
   - Define safe Git GC/repack policy so historical commits are not accidentally removed.
   - Defer Git LFS to a separate task unless needed immediately.

3. **Integrate Git cache into the evaluator**
   - Replace direct evaluator `git clone` operations with the cache abstraction.
   - Preserve existing semantics around base commit checkout, patches, `git diff`, resets, and cleanup.
   - Ensure cached repositories are usable from host-run and containerized evaluations.
   - Add fallback behavior when the cache is unavailable or corrupt.
   - Verify repeated evaluation of the same commit performs no GitHub network transfer.

4. **Deploy and configure `apt-cacher-ng`**
   - Choose host/container deployment.
   - Bind its cache storage to `/srv/swe-cache/apt`.
   - Configure appropriate upstream repository behavior.
   - Establish policy for metadata freshness versus long-lived `.deb` objects.
   - Decide how HTTPS repositories are handled without TLS interception.
   - Add logging and basic cache statistics.

5. **Integrate the APT cache into evaluator containers/builds**
   - Inject `Acquire::http::Proxy` configuration automatically.
   - Avoid modifying individual `apt-get install` commands.
   - Handle host-to-container addressing consistently.
   - Ensure builds can optionally bypass the proxy for debugging.
   - Verify that a Docker cache miss followed by an `apt-get install` retrieves previously seen `.deb` files locally.

6. **Deploy `zot` as the OCI pull-through cache**
   - Bind persistent blob storage to `/srv/swe-cache/oci`.
   - Configure Docker Hub first.
   - Add upstream mappings for registries we actually encounter, such as GHCR, Quay, MCR, ECR Public, and `registry.k8s.io`.
   - Enable settings needed to preserve image/digest compatibility.
   - Define authentication handling for public versus private registries.
   - Establish blob retention and garbage-collection policy.

7. **Integrate OCI caching with Docker/containerd**
   - Configure the Docker daemon or container runtime to use the local mirror.
   - Determine which registries can be redirected directly and which require host-specific configuration.
   - Ensure ordinary commands such as `docker pull ubuntu:24.04` remain unchanged from the evaluator’s perspective.
   - Validate behavior for tags and digest-pinned images.
   - Verify that images can be purged from Docker and subsequently restored entirely from the local OCI cache.

8. **Handle multi-registry edge cases**
   - Test `docker.io`, `ghcr.io`, `quay.io`, `mcr.microsoft.com`, `registry.k8s.io`, and other observed registries.
   - Handle registry authentication/token flows.
   - Decide what happens for unsupported or private registries.
   - Define bypass/fallback behavior rather than letting an unavailable cache block evaluations.

9. **Add a thin `swe-cache` orchestration layer**
   - Do not reimplement Git, APT, or OCI protocols.
   - Provide lifecycle/configuration commands around the three caches.
   - Likely commands:
     - `swe-cache git ensure-repo`
     - `swe-cache git ensure-commit`
     - `swe-cache git checkout`
     - `swe-cache status`
     - `swe-cache stats`
     - `swe-cache gc`
     - `swe-cache doctor`

   - Centralize paths, endpoints, health checks, and evaluator configuration.

10. **Add cache observability**
    - Track cache hit/miss counts.
    - Track bytes downloaded from upstream versus served locally.
    - Track disk consumption by Git, OCI, and APT independently.
    - Track upstream fetch duration.
    - Make it possible to answer: “Why was this evaluation slow?”
    - Prefer simple logs/CLI output initially over a monitoring stack.

11. **Define retention and garbage-collection policies**
    - Git: protect required historical objects; conservative GC.
    - OCI: retain blobs based on age/access/size limits.
    - APT: evict old packages according to configurable storage limits.
    - Set an overall cache disk budget.
    - Avoid one cache class consuming the entire volume.
    - Make manual purge available independently per cache.

12. **Add cache integrity and recovery**
    - Detect corrupt Git mirrors and rebuild them safely.
    - Detect invalid OCI cache/storage state.
    - Handle interrupted APT downloads.
    - Use atomic creation/rename patterns where possible.
    - Make cache corruption degrade into an upstream refetch rather than an evaluator failure.

13. **Handle concurrency**
    - Multiple evaluators may request the same Git repo/commit simultaneously.
    - Multiple Docker pulls may request the same blob simultaneously.
    - Multiple APT clients may fetch the same package simultaneously.
    - Add locking only where underlying tools do not already provide sufficient protection.
    - Avoid global locks that serialize unrelated evaluations.

14. **Add Git LFS support as a separate optional phase**
    - Detect repositories using LFS.
    - Decide whether to reuse `gitcache`’s approach or add a dedicated LFS object cache.
    - Keep this out of the first implementation unless an actual benchmark repository requires it.

15. **Test cold, warm, and destructive-cache scenarios**
    - Cold run: nothing cached.
    - Warm run: everything cached.
    - Docker images deleted but OCI cache retained.
    - Docker build cache deleted.
    - `/var/lib/docker` replaced entirely.
    - Git workspace deleted but Git mirror retained.
    - Evaluator requests historical Git commit already cached.
    - APT metadata refreshed while package payload remains cached.
    - Upstream temporarily unavailable after cache warm-up.

16. **Benchmark before/after**
    - Measure Git clone/checkout time.
    - Measure Docker image acquisition time.
    - Measure APT install time.
    - Measure complete evaluator setup time.
    - Measure upstream bytes transferred.
    - Compare cold, warm, and partially warm states.
    - Use this data to decide whether any more invasive caching is worthwhile.

17. **Only after that, consider advanced optimizations**
    - BuildKit persistent/exported cache.
    - Prewarming commonly used Git repos/images/packages.
    - Cache snapshots/backups.
    - Shared cache across multiple machines.
    - Content-addressed storage deduplication beyond what Git/OCI already provide.
    - A unified service API if the three-tool arrangement proves operationally awkward.

I’d implement in four milestones: **Git cache first**, then **APT**, then **OCI/zot**, and finally **orchestration, metrics, GC, and hardening**. That keeps each performance improvement independently useful and avoids turning this into a large proxy project before we know which cache delivers the biggest gains.

---

apt-cacher-ng runs as a server. As a matter of fact, I have that server running already. Will the zot and git caches run as servers too?

---

**zot: yes, it runs as a server. Git cache: it depends on the implementation.**

For the setup we’ve been discussing, I’d think of it like this:

```text
apt clients  ─────HTTP────▶ apt-cacher-ng server
docker       ─────OCI─────▶ zot registry server
git          ──local/wrap─▶ git mirror cache
```

`zot` is definitely server-shaped. It exposes an OCI registry endpoint over HTTP/HTTPS, stores blobs/manifests persistently, and Docker/containerd are configured to talk to it as a registry mirror or pull-through cache. So operationally it looks very similar to `apt-cacher-ng`: a daemon listening on a port with a persistent cache directory.

The Git side is different. The `seeraven/gitcache` project we discussed is **not primarily a network server**. It is a wrapper around the local `git` executable. When you run something like:

```bash
git clone https://github.com/pixijs/pixijs.git
```

the wrapper creates or updates a local bare mirror, then rewrites the clone to use that local mirror. It uses filesystem storage and locking rather than exposing a Git HTTP service. ([GitHub][1])

So with `gitcache`, the topology is more like:

```text
evaluator process
      │
      ▼
gitcache wrapper
      │
      ├── local bare mirror
      │      /srv/swe-cache/git/...
      │
      └── GitHub, only when mirror needs data
```

rather than:

```text
git clone ──HTTP──▶ git-cache-server:port
```

For our evaluator, I actually prefer the non-server Git approach. We already control repository acquisition, so having the evaluator directly maintain bare mirrors is simpler and avoids adding another network protocol and daemon.

That would give us:

```text
Host
│
├── apt-cacher-ng        server :3142
│     └── /srv/swe-cache/apt
│
├── zot                  server :5000 or similar
│     └── /srv/swe-cache/oci
│
└── Git mirror manager   no listening port
      └── /srv/swe-cache/git
```

The Git mirror manager could just be code inside the evaluator or a small CLI:

```bash
swe-cache git ensure \
    https://github.com/pixijs/pixijs.git \
    <commit>
```

Then it checks:

```text
mirror exists?
   │
   ├── no  → clone --mirror from GitHub
   │
   └── yes
          │
          ▼
   commit exists?
      │       │
     yes      no
      │       │
      │      fetch
      │       │
      └── local checkout
```

This has one nice operational property: **only two cache daemons need to stay running**.

```text
Persistent services:
    apt-cacher-ng
    zot

Persistent filesystem cache:
    Git bare mirrors
```

I would only make the Git cache a server if we later want **multiple machines** to share the same Git cache. At that point, exposing the mirrors through Git smart HTTP, SSH, or a purpose-built caching service could make sense.

For a single SWE-bench host, though, I’d keep Git filesystem-local. It is simpler, faster, and has fewer failure modes.

[1]: https://github.com/seeraven/gitcache?utm_source=chatgpt.com "GitHub - seeraven/gitcache: Local cache for git repositories to speed up working with large repositories and multiple clones. · GitHub"

---

I will like all implementations to be wrapped a single built & portable binary which starts the Docker container containing apt-cacher-ng and zot services managed by supervisord. If maintenance is needed install and start crond as a third service managed by supervisord. The binary will also be called to execute git clones.

What do you think?

---

I think that is a strong design, with one adjustment: make the portable binary the **control plane**, and keep the container purely as the **cache-service runtime**.

That gives you a clean separation:

```text
cachectl binary
│
├── start
│    └── docker run cache-services
│          └── supervisord
│               ├── apt-cacher-ng
│               ├── zot
│               └── crond        # only if actually needed
│
├── stop
├── status
├── doctor
├── stats
├── gc
│
└── git clone / git ensure
     └── host-side Git mirror cache
```

I like this more than making everything a daemon.

The binary could own a persistent root such as:

```text
~/.cache/swe-cache/
# or
/var/lib/swe-cache/

├── apt/
├── zot/
├── git/
├── config/
└── logs/
```

and mount only the relevant pieces into the service container:

```text
host                              container

/var/lib/swe-cache/apt     ───▶   /var/cache/apt-cacher-ng
/var/lib/swe-cache/zot     ───▶   /var/lib/zot
/var/lib/swe-cache/config  ───▶   /etc/swe-cache
```

The Git cache does **not** need to be mounted into that container unless some later feature requires it.

### Why I like the single-binary wrapper

From the user's perspective, installation becomes essentially:

```bash
curl/install swe-cache
sudo mv swe-cache /usr/local/bin/

swe-cache start
```

Then:

```bash
swe-cache status
swe-cache git clone https://github.com/pixijs/pixijs.git ./pixijs
```

The user doesn't have to know how to install or configure:

```text
apt-cacher-ng
zot
supervisord
cron
```

The binary controls the known service image and configuration.

That also gives us a stable interface while we can change the internals later.

For example, today:

```text
swe-cache start
    ↓
Docker + supervisord
```

but we could eventually support:

```text
swe-cache start --runtime podman
```

without changing the rest of the evaluator.

### I would build the service image ourselves

Something along these lines:

```dockerfile
FROM debian:stable-slim

RUN apt-get update && apt-get install -y \
    apt-cacher-ng \
    supervisor \
    cron \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY zot /usr/local/bin/zot

COPY supervisord.conf /etc/supervisor/conf.d/swe-cache.conf

ENTRYPOINT ["supervisord", "-n", "-c", "/etc/supervisor/supervisord.conf"]
```

Conceptually:

```ini
[supervisord]
nodaemon=true

[program:apt-cacher-ng]
command=/usr/sbin/apt-cacher-ng -c /etc/apt-cacher-ng ForeGround=1
autorestart=true

[program:zot]
command=/usr/local/bin/zot serve /etc/zot/config.json
autorestart=true

[program:cron]
command=/usr/sbin/cron -f
autorestart=true
```

I would not necessarily enable `cron` by default, though.

### Cron should probably be optional

There are two ways to handle maintenance.

One is:

```text
supervisord
   └── crond
         ├── apt cache cleanup
         ├── zot cleanup
         └── other housekeeping
```

That works.

But because we already have a control binary, I would first ask whether periodic maintenance really needs an always-running scheduler.

For example, we could do opportunistic maintenance:

```text
swe-cache start
      │
      ├── last maintenance > 24h?
      │       └── run maintenance
      │
      └── start services
```

or:

```text
swe-cache git clone ...
      │
      └── occasionally run lightweight Git maintenance
```

That avoids another service.

If zot or apt-cacher-ng genuinely require periodic jobs, then adding `crond` is completely reasonable.

So I'd make it:

```text
supervisord
├── apt-cacher-ng
├── zot
└── crond                 optional
```

rather than assuming from the beginning that cron must exist.

### The binary should own configuration generation

I would avoid having users manually edit container configuration.

Instead:

```bash
swe-cache start
```

generates things like:

```text
config/
├── apt-cacher-ng.conf
├── zot.json
├── supervisord.conf
└── cron.d/
```

from binary defaults plus perhaps a single user config:

```toml
cache_root = "/var/lib/swe-cache"

[apt]
enabled = true
port = 3142

[oci]
enabled = true
port = 5000

[git]
enabled = true

[maintenance]
enabled = true
interval = "24h"
```

That means the binary controls the whole deployment contract.

### I would also make the container disposable

This is important.

The service container itself should have essentially zero valuable state:

```text
container dies/recreated
        │
        ▼
nothing significant lost
```

All meaningful state lives in bind mounts:

```text
/var/lib/swe-cache/apt
/var/lib/swe-cache/zot
```

So:

```bash
docker rm -f swe-cache-services
swe-cache start
```

should recreate the environment instantly.

Even upgrading the service image becomes simple:

```text
old service container
       ↓
remove
       ↓
new image
       ↓
mount same persistent caches
```

### Git should stay host-side

I would strongly keep the Git execution in the binary rather than run Git through the service container.

Something like:

```bash
swe-cache git clone \
    https://github.com/pixijs/pixijs.git \
    ./pixijs
```

internally becomes:

```text
normalize URL
    ↓
mirror path:
    /var/lib/swe-cache/git/github.com/pixijs/pixijs.git
    ↓
mirror exists?
    ├── no → git clone --mirror
    └── yes
    ↓
ensure desired refs/commit
    ↓
clone locally into destination
```

And eventually:

```bash
swe-cache git clone \
    --commit <sha> \
    https://github.com/pixijs/pixijs.git \
    ./pixijs
```

could provide the SWE-bench-specific optimization:

```text
commit exists in mirror
       │
       ├── yes → ZERO GitHub access
       │
       └── no  → fetch upstream
```

### One binary does not necessarily mean one implementation language

I'd probably write the wrapper in **Go**.

Main reasons:

```text
single static-ish executable
easy subprocess execution
good filesystem APIs
good locking primitives
easy Docker CLI/process interaction
cross-platform builds
straightforward config handling
```

And importantly, we do **not** have to embed zot as a Go library.

The binary is just orchestrating:

```text
Docker
Git
filesystem
config
locks
```

That keeps the implementation relatively small.

### I would avoid Docker SDK integration initially

Instead of having the binary talk directly to the Docker API, I'd start with:

```bash
docker inspect ...
docker pull ...
docker run ...
docker stop ...
```

from the Go process.

That reduces dependencies and gives behavior users can reproduce manually.

If that later becomes limiting, we can switch the backend to the Docker API without changing the CLI.

### The service image should be versioned together with the binary

For example:

```text
swe-cache 0.3.0
        │
        └── expects
            ghcr.io/our-project/swe-cache-services:0.3.0
```

Then:

```bash
swe-cache start
```

can ensure the corresponding service image exists.

This prevents compatibility problems like:

```text
new binary
+
old zot config schema
+
random apt-cacher-ng version
```

We control the complete tested combination.

### There's one bootstrap problem worth thinking about

If the binary starts the caching infrastructure from a Docker image, then the **first pull of the cache-service image itself cannot benefit from zot**.

That's fine.

It is a tiny bootstrap cost:

```text
Internet
   │
   ▼
swe-cache-services image
   │
   ▼
zot now running
   │
   ▼
future OCI pulls cached
```

We could make the service image reasonably small.

Alternatively, the binary could embed the image archive eventually, but I would absolutely not do that initially. It makes releases enormous and complicates updates.

### The resulting UX could be very pleasant

Something along these lines:

```bash
# Start cache infrastructure
swe-cache start

# Inspect it
swe-cache status

APT cache:
  running
  http://127.0.0.1:3142

OCI cache:
  running
  http://127.0.0.1:5000

Git cache:
  /var/lib/swe-cache/git

Disk:
  apt     8.4 GB
  oci    31.7 GB
  git    12.2 GB
```

Then:

```bash
swe-cache git clone \
  https://github.com/pixijs/pixijs.git \
  ./pixijs
```

And:

```bash
swe-cache stop
```

stops only the service container while preserving all caches.

Potentially:

```bash
swe-cache destroy
```

removes the service container but still preserves cache data unless explicitly told:

```bash
swe-cache purge --all
```

I'd be conservative around deletion.

### I'd structure the binary internally like this

```text
cmd/
    start
    stop
    status
    doctor
    git
    gc

internal/
    docker/
    gitcache/
    config/
    service/
    maintenance/
    locks/
    paths/
```

The conceptual boundary remains:

```text
                    swe-cache binary
                           │
          ┌────────────────┴───────────────┐
          │                                │
          ▼                                ▼
  infrastructure control              Git operations
          │                                │
        Docker                         local Git
          │                                │
          ▼                                ▼
   service container                bare mirrors
          │
    supervisord
     │       │
     ▼       ▼
   ACNG     zot
```

I think this is cleaner than our previous notion of a generic `swe-cache manager`. It becomes a concrete product boundary: **one executable is the only thing the evaluator or user needs to interact with**.

The first implementation milestone I'd now define as the binary skeleton plus `start/stop/status`, launching a reproducible service image containing `apt-cacher-ng + zot + supervisord`. Git caching can then be added to the same executable without changing that architecture.

---

Here’s the updated implementation task breakdown incorporating the **single portable binary**, **service container**, **supervisord**, optional **crond**, host-side Git cache, and disposable-container model.

1. **Define the product boundary and persistent layout**
   - The only user-facing executable is `swe-cache`.
   - `swe-cache` owns configuration, lifecycle, health checks, Git cache operations, maintenance, and diagnostics.
   - Persistent data lives outside Docker, e.g.:

     ```text
     /var/lib/swe-cache/
     ├── apt/
     ├── zot/
     ├── git/
     ├── config/
     └── logs/
     ```

   - `/var/lib/docker` is explicitly disposable.
   - Define permissions, ownership, defaults, and override mechanisms.

2. **Create the `swe-cache` binary skeleton**
   - Prefer Go for a portable single executable.
   - Establish CLI structure:

     ```text
     swe-cache start
     swe-cache stop
     swe-cache restart
     swe-cache status
     swe-cache doctor
     swe-cache stats
     swe-cache gc
     swe-cache git ...
     ```

   - Add version reporting and structured exit codes.
   - Keep the initial binary independent of Docker SDK libraries where practical.

3. **Define the service-container image**
   - Build one versioned image containing:
     - `apt-cacher-ng`
     - `zot`
     - `supervisord`
     - `crond`/`cron`, if maintenance requires it
     - certificates and minimal runtime dependencies

   - `supervisord` runs as PID 1.
   - The image itself contains no valuable persistent state.
   - Pin and test component versions.

4. **Configure supervisord**
   - Manage:

     ```text
     apt-cacher-ng
     zot
     crond   # optional
     ```

   - Both cache services should run in foreground mode where supported.
   - Configure automatic restart behavior.
   - Route logs predictably.
   - Ensure one failed service does not silently make the overall container appear healthy.

5. **Implement `swe-cache start`**
   - Detect Docker availability.
   - Resolve/create cache directories.
   - Generate runtime configuration.
   - Ensure the matching service image exists.
   - Pull it if necessary.
   - Start the service container with bind mounts.
   - Publish only required ports.
   - Make repeated `start` calls idempotent.

6. **Implement `stop`, `restart`, and container recreation**
   - `stop` stops services without deleting cache contents.
   - `restart` safely recreates services.
   - Support replacing the service container while retaining:

     ```text
     apt/
     zot/
     git/
     ```

   - Treat the service container itself as disposable.

7. **Version the binary and service image together**
   - Example:

     ```text
     swe-cache 0.3.0
     ↕
     ghcr.io/.../swe-cache-services:0.3.0
     ```

   - Define compatibility rules.
   - Avoid arbitrary combinations of binary and container versions.
   - Add upgrade behavior for configuration schema changes.

8. **Implement configuration generation**
   - The binary generates:

     ```text
     apt-cacher-ng configuration
     zot configuration
     supervisord configuration
     cron configuration, if enabled
     ```

   - Avoid requiring users to manually edit service-specific config files.
   - Optionally support one top-level config such as:

     ```toml
     cache_root = "/var/lib/swe-cache"

     [apt]
     enabled = true
     port = 3142

     [oci]
     enabled = true
     port = 5000

     [git]
     enabled = true

     [maintenance]
     enabled = false
     ```

9. **Integrate the existing apt-cacher-ng setup**
   - Since apt-cacher-ng already exists in the environment, decide whether:
     - `swe-cache` adopts/manages that existing instance, or
     - the final packaged solution always runs its own containerized instance.

   - Preserve any useful existing cache data if migrating.
   - Bind persistent APT data into the service container.
   - Validate metadata refresh and `.deb` caching behavior.

10. **Configure zot pull-through caching**
    - Store zot state under:

      ```text
      /var/lib/swe-cache/zot
      ```

- Configure Docker Hub first.
- Then add required upstreams such as:

  ```text
  ghcr.io
  quay.io
  mcr.microsoft.com
  registry.k8s.io
  public.ecr.aws
  ```

- Preserve digest semantics.
- Define authentication behavior for private registries separately.

11. **Integrate Docker with zot**

- Configure Docker/containerd to use the local cache where supported.
- Keep ordinary evaluator commands unchanged:

  ```bash
  docker pull ubuntu:24.04
  ```

- Handle registry-specific mirror configuration where necessary.
- Provide clear diagnostics when a registry cannot be transparently mirrored.

12. **Implement `swe-cache status`**

- Report:

  ```text
  service container state
  apt-cacher-ng state
  zot state
  optional cron state
  endpoints
  persistent cache paths
  binary/service-image versions
  ```

- Distinguish “container running” from “services healthy”.

13. **Implement `swe-cache doctor`**

- Validate:
  - Docker installed/running
  - cache directories writable
  - required ports available
  - service container reachable
  - apt-cacher-ng responds
  - zot responds
  - Git executable available
  - upstream connectivity where appropriate
  - Docker mirror configuration is correct

- Produce actionable failures rather than raw subprocess output.

14. **Implement host-side Git mirror storage**

- Keep Git outside the service container.
- Normalize repository URLs into deterministic mirror paths:

  ```text
  /var/lib/swe-cache/git/github.com/pixijs/pixijs.git
  ```

- Use bare mirrors.
- Do not require a long-running Git server.

15. **Implement `swe-cache git clone`**

- Example:

  ```bash
  swe-cache git clone \
    https://github.com/pixijs/pixijs.git \
    ./pixijs
  ```

- On first use:

  ```text
  git clone --mirror upstream → cache
  ```

- On subsequent use:

  ```text
  local mirror → destination
  ```

- Preserve the real upstream URL as `origin` where appropriate.

16. **Implement commit-aware Git caching**

- Support:

  ```bash
  swe-cache git clone \
    --commit <sha> \
    <repo> \
    <destination>
  ```

- Before contacting GitHub:

  ```bash
  git cat-file -e "<sha>^{commit}"
  ```

- If present, perform zero upstream access.
- If absent, fetch only what is necessary.
- Optimize specifically for historical SWE-bench commits.

17. **Define Git clone strategy**

- Decide between:
  - local hardlinked clone
  - alternates/reference clone
  - fully independent clone

- Favor fast local cloning while avoiding fragile long-lived dependencies.
- Ensure temporary evaluator workspaces remain safe while mirrors are maintained.

18. **Add Git locking and concurrency controls**

- Prevent simultaneous first-time creation of the same mirror.
- Serialize conflicting fetch/repack operations per repository.
- Do not globally serialize unrelated repositories.
- Recover cleanly from interrupted mirror creation.

19. **Define Git maintenance policy**

- Repack when useful.
- Avoid aggressive pruning of historical objects.
- Never let maintenance remove commits still useful to benchmarks.
- Consider Git LFS separately rather than bundling it into the first version.

20. **Decide whether crond is actually required**

- First prefer maintenance initiated by `swe-cache`, for example:

  ```text
  swe-cache start
    → run overdue lightweight maintenance
  ```

- Use `crond` only if cache services need independent periodic work.
- If needed, make it a third supervisord-managed service.
- Keep maintenance jobs idempotent.

21. **Implement `swe-cache gc`**

- Manage each cache independently:

  ```text
  swe-cache gc apt
  swe-cache gc oci
  swe-cache gc git
  swe-cache gc --all
  ```

- Define safe defaults.
- Prefer retention by age/size over destructive full wipes.
- Never make ordinary GC equivalent to “delete everything”.

22. **Implement cache size limits**

- Support individual budgets:

  ```text
  apt: 20 GB
  zot: 100 GB
  git: 50 GB
  ```

- Optionally enforce a global cache budget.
- Prevent one subsystem from exhausting the disk.

23. **Implement `swe-cache stats`**

- Report at least:

  ```text
  cache disk usage
  number of Git mirrors
  upstream vs local Git clone behavior
  APT cache usage
  OCI cache usage
  service uptime
  ```

- Add hit/miss and downloaded-byte metrics where underlying services expose them reliably.

24. **Add service health checks**

- Test apt-cacher-ng endpoint.
- Test zot OCI endpoint.
- Validate supervisord process status.
- Surface degraded states such as:

  ```text
  apt healthy
  zot unhealthy
  cron disabled
  ```

25. **Handle bootstrap behavior**

- Accept that the initial service-image pull cannot use zot.
- Keep the service image reasonably small.
- Do not embed the Docker image into the binary initially.
- After startup, normal OCI traffic can use the cache.

26. **Implement safe configuration of evaluator containers**

- Inject the apt-cacher-ng proxy automatically.
- Configure access to host-published cache endpoints.
- Avoid modifying individual project install scripts.
- Make cache bypass possible for troubleshooting.

27. **Define failure and fallback behavior**

- If apt-cacher-ng is unavailable, decide whether APT should fail or bypass.
- If zot is unavailable, allow direct registry access when policy permits.
- If a Git mirror is corrupted, quarantine/rebuild it.
- Cache failures should not silently corrupt evaluation results.

28. **Test container destruction scenarios**

- Delete the service container and recreate it.
- Delete all Docker images.
- Delete Docker build cache.
- Replace `/var/lib/docker`.
- Confirm:

  ```text
  APT cache survives
  zot cache survives
  Git mirrors survive
  ```

29. **Test cold/warm behavior**

- Cold:

  ```text
  GitHub + registry + apt upstream used
  ```

- Warm:

  ```text
  Git clone local
  OCI blobs local
  APT packages local
  ```

- Partially warm:
  validate each cache independently.

30. **Benchmark the complete system**

- Measure:
  - first Git clone vs repeated clone
  - first image pull vs pull after Docker purge
  - first apt install vs repeated install
  - full evaluator startup time
  - upstream bytes transferred

- Use the measurements to prioritize further work.

31. **Package the binary for portability**

- Build release binaries for required architectures.
- Prefer minimal runtime dependencies beyond:

  ```text
  Docker
  Git
  ```

- Provide checksums/signatures as appropriate.
- Keep installation to essentially copying one executable.

32. **Only after the base system is stable, consider extensions**

- Podman support.
- Git LFS caching.
- BuildKit cache persistence/export.
- Cache prewarming.
- Remote/shared caches across machines.
- Backup/restore.
- Metrics export.
- Multi-user permissions.
- Native Docker API integration instead of shelling out.

I’d group those into five milestones:

```text
Milestone 1
Portable binary + service image + supervisord + start/stop/status

Milestone 2
apt-cacher-ng + zot fully operational and persistent

Milestone 3
Git bare-mirror cache + commit-aware swe-cache git clone

Milestone 4
doctor + stats + maintenance + GC + concurrency + recovery

Milestone 5
Evaluator integration + destructive-cache tests + benchmarking
```

That sequencing gets the core architecture working early without prematurely building maintenance, metrics, or advanced cache features before the three caching paths themselves are proven.
