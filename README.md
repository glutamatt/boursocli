# 🏦 boursocli — fork durci (lecture seule)

CLI agent-first pour un compte BoursoBank (ex-Boursorama Banque)
**personnel**, **en lecture seule**.

Ce dépôt est un fork de
[ThomasMarcelin754/boursocli](https://github.com/ThomasMarcelin754/boursocli).
La branche `hardening` le rend sûr à confier à un agent (Claude Code…) :
pas de npm au runtime, pas de cookies sur disque, une seule porte de sortie
réseau, GET uniquement. Le détail est dans [Sécurité](#sécurité) et dans
`SECURITY.md`.

> Un seul titulaire, son propre compte, au rythme humain. Pas une ferme de
> scraping, pas multi-locataire. Voir `AGENTS.md` pour les règles de
> contribution.

## Installation

Depuis les sources, sur un commit que vous avez relu :

```sh
git clone https://github.com/glutamatt/boursocli && cd boursocli
git checkout hardening          # ou un commit précis
make build && ./boursocli --help
make check                      # tests, lint (gosec), govulncheck, vérif du vendoring
```

Il faut :

- **Go ≥ 1.21** (avec `GOTOOLCHAIN=auto`, le défaut, Go télécharge la
  toolchain 1.25.10 requise par `go.mod`) ;
- **Node ≥ 22.13** sur le `PATH` (pour `node:sqlite`) — **pas npm** ;
- sous Linux, **`secret-tool`** (paquet `libsecret-tools`) pour lire la clé
  « Chrome Safe Storage » du trousseau GNOME (`kwallet-query` sous KDE) ;
- **Chrome connecté à BoursoBank**.

Pas de cask Homebrew dans ce fork : celui d'origine retirait le drapeau de
quarantaine macOS et ne vérifiait pas la signature cosign. Les binaires de
release sont signés : vérifiez `checksums.txt` avec cosign avant usage.

**Docker** (`make docker`) : l'extraction des cookies lit le trousseau de
l'OS *hôte*, indisponible dans un conteneur. Un `config.json` monté ne
contient que le bearer (≤ 24 h) : les commandes du plan Bearer marchent
jusqu'à son expiration.

```sh
docker run --rm --user "$(id -u)" -v "$HOME/.config/boursocli:/cfg" \
  boursocli:dev --config /cfg/config.json accounts
```

`--user` : le fichier doit appartenir à l'utilisateur du conteneur.

## Authentification (sans mot de passe, sans secret d'environnement)

`boursocli` ne demande jamais votre mot de passe et ne lit aucun secret
depuis l'environnement. Il lit la session BoursoBank *existante* dans votre
profil **Chrome** local, puis récupère le bearer API (24 h) sur le dashboard.

- Les cookies sont déchiffrés par [sweet-cookie](internal/auth/sweetcookie/VENDOR.md),
  **intégré au binaire** (copie vérifiée du paquet npm 0.2.0). node tourne
  dans un dossier privé temporaire, avec un environnement minimal, et ce
  dossier est effacé à la fin — même si node est tué.
- Les cookies restent **en mémoire**. Ils ne sont jamais écrits sur disque.
- Sur disque (`config.json`, fichier `0600`) : seulement le bearer, sa date
  d'expiration, le user hash et vos réglages. Le CLI refuse un fichier
  lisible par d'autres, un lien symbolique, un fichier qui n'est pas à vous,
  et un dossier où d'autres peuvent écrire (sauf dossier « sticky » comme
  `/tmp`).
- `config show` masque les secrets ; `config wipe` efface le bearer.
- `--refresh` force un nouveau bearer depuis Chrome.

### Profil Chrome

Sans profil épinglé, le premier lancement choisit le profil Chrome dont la
session BoursoBank est la plus récente, puis **l'épingle** dans la config :
le scan de tous les profils n'a lieu qu'une fois. Si ce profil n'a plus de
session vivante, le CLI refait le scan. Pour choisir vous-même (épinglage
définitif) :

```sh
boursocli config set chrome_profile "Profile 9"   # nom ou chemin
```

### Durée de session

BoursoBank est sous DSP2/SCA : aucune session n'est éternelle et aucune
reconnexion ne peut être scriptée. C'est **Chrome** qui porte la session.

- Quand le bearer expire ou est refusé, le CLI en prend un nouveau depuis la
  session Chrome (une requête GET). Il ne prolonge jamais la session lui-même.
- Si la session Chrome est morte : reconnectez-vous dans Chrome (en cochant
  « Se souvenir de moi »), puis relancez la commande.
- Le renouvellement serveur (`POST session/auth/refresh`) est **désactivé**
  par défaut, car il prolonge la session à la banque. Pour l'autoriser :
  `--allow-session-refresh`, ou `config set allow_session_refresh true`.

## Utilisation

La sortie est **agent-first** : JSON sur stdout par défaut, diagnostics sur
stderr, code de sortie `0`/`1`. `--format table` pour les humains,
`--quiet`/`--debug`.

```sh
boursocli accounts                      # comptes + soldes (JSON)
boursocli accounts --format table
boursocli operations --account cav      # opés récentes (Bearer, 30 plus récentes)
boursocli export --account cav --out ops.csv   # historique complet CSV (nouveau fichier)
boursocli positions --account ord       # portefeuille titres
boursocli ord-orders --account ord
boursocli ord-fiscalite --account ord --year 2026
boursocli documents --account ord       # relevés / relevés CAV
boursocli ord-ost --account ord
boursocli transfers --account cav
boursocli budgets ; boursocli budget-movements --account cav
boursocli incidents --account cav
boursocli version ; boursocli --version
```

`--account` prend un `accountKey` (32-hex) ou un type : `cav` | `ord` |
`card` | `pea`. Ambiguïté ou aucune correspondance → une erreur explicite
qui liste les choix.

`export --out` crée **un nouveau fichier** : il n'écrase jamais un fichier
existant et ne suit jamais un lien symbolique.

Les échecs sont toujours explicites : un non-200, une erreur de décodage ou
une dérive de schéma sort en `1` avec `{"ok":false,"error":…}`.

## Cibles Make

```sh
make build          # go build
make test           # go test ./... -race
make lint           # golangci-lint (incl. gosec)
make verify-vendor  # sweet-cookie intégré == tarball npm (intégrité + diff octet par octet)
make sec            # lint + govulncheck + verify-vendor
make check          # fmt vet test lint vulncheck verify-vendor — la porte complète
```

## Sécurité

### Ce que garantit le code

- **Lecture seule.** Une garde sur le transport HTTP voit chaque requête et
  chaque redirection avant envoi : **GET uniquement**. Le seul POST existant
  (renouvellement de session) est refusé sauf autorisation explicite.
- **Une seule destination.** HTTPS vers `clients.boursobank.com`,
  `api.boursobank.com`, `clients.boursorama.com` — hôtes exacts, pas de
  sous-domaine. Pas de segment `..`, pas d'octet encodé dans le chemin.
  Une redirection ailleurs est refusée.
- **Entrées vérifiées.** Symboles, codes d'indice, ids, années, dates et
  clés de compte sont vérifiés avant toute session : `quote --symbol` ne
  peut lire qu'une cotation.
- **Pas de npm, pas de cookies sur disque** (voir Authentification).
- **Chaîne de build figée** : actions GitHub épinglées par SHA, outils par
  version, images Docker par digest.

### Ce que le code ne peut pas garantir

- **La banque ne connaît pas de « lecture seule ».** Avec le bearer ou les
  cookies Chrome, l'API BoursoBank permet aussi de passer des ordres et de
  faire des virements. La lecture seule vient de ce binaire, pas de la
  banque.
- **Tout processus lancé sous votre compte** peut lire `config.json` (le
  bearer, 24 h max) et déchiffrer lui-même les cookies Chrome. C'était déjà
  vrai avant ce CLI ; il ne l'aggrave pas, mais ne peut pas l'empêcher.
- **Injection de prompt.** Les libellés d'opérations et la messagerie
  (`messages`) viennent de tiers. Un agent qui les lit peut y trouver des
  instructions. Ne donnez jamais à cet agent un moyen d'écrire à la banque.

### Utilisation avec Claude Code

Des garde-fous, pas une frontière de sécurité (un agent qui a un shell peut
les contourner). Exemple de `.claude/settings.json` :

```json
{
  "permissions": {
    "allow": [
      "Bash(boursocli accounts:*)",
      "Bash(boursocli operations:*)",
      "Bash(boursocli positions:*)",
      "Bash(boursocli export:*)"
    ],
    "deny": [
      "Read(~/.config/boursocli/**)",
      "Bash(boursocli config set:*)"
    ]
  }
}
```

Laissez `allow_session_refresh` à `false`. Le drapeau
`--allow-session-refresh` peut se placer n'importe où dans la commande : une
règle par préfixe ne suffit pas à l'interdire, relisez les commandes de
l'agent.

## État

- 21 commandes de lecture, validées sur un compte réel par l'auteur
  d'origine (2026-05-20). Le durcissement de ce fork est couvert par des
  tests hors ligne (serveurs httptest, faux profil Chrome) ; revalidez sur
  votre compte après installation.
- Pas de virement, ni d'aucune écriture : c'est voulu.
- Ce dépôt ne contient que le code du client : pas de spécification d'API
  tierce.
