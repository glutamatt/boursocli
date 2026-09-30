# Changelog

Tous les changements notables de ce projet. Format :
[Keep a Changelog](https://keepachangelog.com), SemVer.

## [Non publié] — fork durci (branche `hardening`)

Lecture seule, sûr à confier à un agent. Voir `SECURITY.md`.

### Sécurité
- **Plus de npm au runtime** : sweet-cookie 0.2.0 est intégré au binaire
  (`internal/auth/sweetcookie`, vérifié par `make verify-vendor`). node
  tourne dans un dossier privé temporaire, effacé même si node est tué,
  avec un environnement minimal (`NODE_OPTIONS`, `NODE_PATH`,
  `SWEET_COOKIE_*` exclus, `PATH` sans entrée relative), `--no-addons`,
  sans `eval`.
- **Garde de sortie** sur le transport : HTTPS, hôtes exacts, GET
  uniquement, chemins sans `..` ni octet encodé, chaque redirection
  vérifiée. Le cookie n'est jamais ajouté à une requête du plan Bearer.
- **Plus de cookies sur disque** (dont `rememberme`) : lus dans Chrome à la
  demande, gardés en mémoire. Une config v1 est nettoyée au chargement.
- **Plus de renouvellement automatique de session** : désactivé par
  défaut (`--allow-session-refresh`, `config set allow_session_refresh`).
- **Config privée** : fichier 0600, à vous, sans lien symbolique ; dossier
  non inscriptible par d'autres ; écriture atomique par fichier temporaire
  aléatoire. Migration v1 : cookies et ancien cache npm supprimés.
- **Entrées vérifiées** avant toute session (symboles, ids, années, dates,
  page, clés de compte).
- Probe : seule une erreur d'authentification relit Chrome ; les autres
  échecs gardent le bearer. Une seule récupération de session par
  processus, jamais sur `trading/`. `USER_HASH` vérifié avant usage.
- CI/release : injection shell du workflow de release corrigée ; actions
  épinglées par SHA, outils par version, images par digest ; cask Homebrew
  retiré. `golang.org/x/net` 0.53.0 → 0.56.0.

### Ajouté
- `config wipe`, `config set allow_session_refresh true|false`.
- Le profil Chrome choisi automatiquement est épinglé dans la config, et
  re-scanné s'il n'a plus de session.
- Symboles `$…` (indices US : `$INDU`, `$COMPX`) acceptés.

### Corrigé (test sur un compte réel, 2026-09-30)
- Types de comptes : livrets (`livret`), PEA (`pea`), assurance-vie (`av`)
  et autres assurances (`assurance`) étaient tous classés `ord`.
- Chaque commande vérifie le type du compte et indique la bonne commande.
- `positions` : colonnes lues par leur titre (le PEA n'a pas « Dernier
  Mvt ») ; variation du jour lue.
- `ord-ost` : l'état vide (aucune OST) n'est plus une erreur.
- `docs --section bourse` : filtre compte + période (`--account`, `--from`,
  `--to`), sans quoi la page ne liste rien ; liens de téléchargement lus.
- `card` : lit la carte dans la liste des comptes (l'endpoint
  `parameterssummary` répond 404).
- Pages HTML servies sans leur tableau : un nouvel essai, puis une erreur
  explicite — plus jamais « 0 document » en silence.

### Ajouté (suite)
- `ord-mouvements --period M-AAAA[,…]` et `availablePeriods`.
- `budget-movements` paginé : suit `?continuationToken=` (lien
  « Mouvements précédents », relevé dans Chrome) jusqu'à `--from`, avec
  dédoublonnage par `data-id` ; `--max-pages`, `pages`, `stoppedBy`.

- `download --url <downloadUrl> --out <fichier.pdf>` : PDF d'un avis
  d'opéré, relevé, IFU, RIB… Liens de téléchargement BoursoBank uniquement,
  réponse vérifiée (`%PDF-`), nouveau fichier seulement.

### Retiré
- `export` : la banque exige désormais un POST avec jeton CSRF ; tout
  l'historique passe par `budget-movements`, en GET.

### Requis
- Node ≥ 22.13 (plus npm).

## [0.1.0-rc.1] — 2026-05-19

Première prerelease : valide le pipeline de release (goreleaser 6
plateformes + checksums + SBOM Syft + signature cosign keyless). Non
destinée à un usage général.

### Ajouté
- 12 commandes de lecture (Bearer JSON + HTML/CSV plan cookie) :
  `accounts`, `operations`, `transfers`, `budgets`, `incidents`,
  `positions`, `ord-orders`, `ord-fiscalite`, `documents`, `ord-ost`,
  `budget-movements`, `export` — toutes validées sur un compte réel (HTTP 200 +
  données réelles, 2026-05-19) — plus les méta `config` et `version`
  (14 commandes cobra au total).
- Auth `chromecookies` bi-domaine (sans mot de passe, sans secret d'env) +
  amorçage bearer ; sortie agent-first ; transport HTTP audité.
- Outillage de sécurité : `.golangci.yml` (incl. `gosec`), `govulncheck`,
  `Makefile` (`make check` = fmt+vet+test+lint+vulncheck).
- Workflow CI ; `internal/version` + `version`/`--version` ; README,
  CHANGELOG.
- Résilience réseau : backoff throttle (sans ré-auth) vs session bank
  `POST _public_/session/auth/refresh` + un retry — deux boucles séparées ;
  URLs à jeton unique jamais réessayées (`CookieOnce`).
- Distribution Homebrew (parité avec le CLI Go de référence) :
  `scripts/release-homebrew.sh` manuel + playbook
  `docs/releasing-homebrew.md` + template `Formula/boursocli.rb` (copie
  vivante dans un repo `homebrew-tap` séparé). `make homebrew VERSION=x`.
  Volontairement PAS de npm/npx (binaire Go → canaux natifs ; wrapper npm =
  surface supply-chain inutile).
- Release : `.goreleaser.yaml` (6 plateformes, sans CGO, checksums, version
  via ldflags — injection vérifiée), `release.yml` déclenché au tag
  (+ workflow_dispatch) ; `Dockerfile` multi-stage (base Node pour
  chromecookies) avec la limite keychain-hôte documentée ; cibles Makefile
  `release-check`/`snapshot`/`docker`.
- Internationalisation : surface utilisateur en français (public fr) ;
  commentaires/identifiants de code restent en anglais (convention Go).

### Corrigé
- Corrections de schéma trouvées en validation réelle (accounts 47 champs &
  `visibility` bool ; operations/transfers objets imbriqués ; positions
  10 colonnes ; cellules ORD = 3 markups distincts ; budget-movements est
  une liste div pas une table ; export renvoie le CSV directement).
- `gosec` G119 : cookie de session ré-attaché en redirect uniquement vers
  les hôtes boursobank/boursorama autorisés.
- 6 CVE atteignables (5 stdlib + `golang.org/x/net`) → Go 1.25.10 +
  x/net v0.53.0.
- Tests unitaires : client (httptest :
  do/resilientGet/Refresh/Bootstrap/allowlist redirect), config, htmlx,
  out, version, helpers purs cli. La CI plancher les paquets critiques
  75–90 % ; couche commandes assurée par la validation de bout en bout sur compte réel (politique
  honnête documentée, pas de % total truqué).

### Sécurité
- **Bug de fuite de cookie en redirect trouvé par notre propre test &
  corrigé** : `CheckRedirect` *retire* désormais activement le cookie de
  session sur tout hôte non autorisé (Go transmet le header Cookie initial
  aux redirections même-hôte:port-différent).
- Écriture atomique de `config.json`, dossier `0700` / fichier `0600`,
  `user_hash` masqué.
- Vérifié : aucun `.env`/secret dans le dépôt, le CLI ne lit aucun
  identifiant depuis l'environnement.
