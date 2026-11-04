# Yélé — Backend

Serveur de mesure et API du projet Yélé. Il joue trois rôles :

1. **Serveur de speedtest** — fournit les points de terminaison contre lesquels
   l'application mobile mesure le débit et la latence.
2. **Collecteur de mesures** — reçoit les résultats de l'application mobile,
   les enrichit et les stocke dans MongoDB.
3. **API du tableau de bord** — expose les données agrégées consommées par le
   site web public, et relaie le micro-service d'analyse.

Ce dépôt est un **fork de [LibreSpeed speedtest-go](https://github.com/librespeed/speedtest-go)**.
Le moteur de speedtest est celui du projet amont ; la couche MongoDB, l'API du
tableau de bord et le proxy IA sont les ajouts Yélé.

---

## Le projet Yélé en un coup d'œil

| Dépôt | Rôle | Techno |
|---|---|---|
| `mobilefront` | Application mobile : réalise les mesures sur le terrain | Flutter / Dart |
| **`mobiletest`** *(ce dépôt)* | Backend : speedtest, collecte, API | Go + MongoDB |
| `new_web` | Tableau de bord public : carte, statistiques | HTML/CSS/JS |
| `ai` | Micro-service d'analyse : anomalies et prévision | Python / FastAPI |

```
  [ Application mobile ]
       |  POST /results/telemetry
       v
  [ CE DÉPÔT ] --------- stocke ---------> [ MongoDB Atlas ]
       |                                          ^
       |  GET /api/dashboard/*                    | lit directement
       v                                          |
  [ Tableau de bord web ]                   [ Service IA ]
       |                                          ^
       +---- GET /api/ai/*  --- proxifié par -----+
```

---

## Prérequis

- **Go >= 1.25**
- Une base **MongoDB** — une instance Atlas gratuite suffit. Elle est
  **obligatoire pour l'API du tableau de bord** : les autres backends de base de
  données héritées de LibreSpeed (PostgreSQL, MySQL, SQLite, BoltDB, MSSQL)
  n'implémentent pas les agrégations `/api/dashboard/*`.

## Démarrage rapide

```bash
git clone <url-du-depot>
cd mobiletest

go mod download

# La chaîne de connexion ne doit JAMAIS être écrite dans settings.toml
export SPEEDTEST_DATABASE_CONNECTION_STRING="mongodb+srv://user:motdepasse@cluster.mongodb.net/"

go run main.go
```

Le serveur écoute sur **http://localhost:8989**.

Vérifier qu'il répond :

```bash
curl http://localhost:8989/getIP
curl http://localhost:8989/api/coverage
```

Compiler un binaire :

```bash
go build -o speedtest .
./speedtest -c settings.toml
```

## Configuration

La configuration se lit dans **`settings.toml`**. Les **secrets se passent par
variables d'environnement**, avec le préfixe `SPEEDTEST_`, et ne doivent jamais
être committés.

| Variable d'environnement | Clé `settings.toml` | Rôle |
|---|---|---|
| `SPEEDTEST_DATABASE_CONNECTION_STRING` | `database_connection_string` | Chaîne MongoDB Atlas — **requis** |
| `SPEEDTEST_IPINFO_API_KEY` | `ipinfo_api_key` | Clé ipinfo.io pour détecter le FAI |
| `SPEEDTEST_STATISTICS_PASSWORD` | `statistics_password` | Mot de passe de la page `/stats` |
| `SPEEDTEST_AI_SERVICE_URL` | `ai_service_url` | URL du service IA. **Vide = `/api/ai/*` désactivé** |
| `SPEEDTEST_DASHBOARD_EMAIL` | `dashboard_email` | Adresse autorisée sur le tableau de bord. **Vide = accès impossible** |
| `SPEEDTEST_DASHBOARD_PASSWORD` | `dashboard_password` | Mot de passe du tableau de bord. **Vide = accès impossible** |

Réglages non sensibles, dans `settings.toml` :

| Clé | Défaut | Rôle |
|---|---|---|
| `listen_port` | `8989` | Port d'écoute |
| `database_type` | `mongodb` | Doit rester `mongodb` pour le tableau de bord |
| `database_name` | `yele_speedtest` | Nom de la base |
| `redact_ip_addresses` | `false` | Anonymise les IP dès l'enregistrement |
| `server_lat` / `server_lng` | `12` / `-1` | Position du serveur (Ouagadougou) |

---

## Les points de terminaison

### Moteur de speedtest (hérité de LibreSpeed)

| Route | Méthode | Rôle |
|---|---|---|
| `/garbage` | GET | Flux de données aléatoires — mesure du débit descendant |
| `/empty` | GET/POST | Absorbe les données — mesure du débit montant |
| `/getIP` | GET | IP du client + informations FAI. Sert aussi de test de vie |
| `/results/telemetry` | POST | **Réception d'une mesure** |
| `/results` | GET | Image PNG récapitulative d'un test |
| `/stats` | GET | Page de statistiques protégée par mot de passe |

Chaque route existe aussi sous le préfixe `/backend/`, par compatibilité avec le
backend PHP historique de LibreSpeed.

### API du tableau de bord (spécifique Yélé)

Ces routes et `/api/ai/*` exigent un jeton obtenu par
`POST /api/dashboard/login {email, password}`, à joindre en en-tête
`Authorization: Bearer <jeton>`. Il est valable sept jours. Changer l'adresse ou
le mot de passe invalide tous les jetons déjà émis.

L'application mobile n'a pas ce jeton : sa carte lit `GET /api/coverage`, public,
qui ne renvoie que la position, l'opérateur, le type de réseau et le débit.

| Route | Renvoie |
|---|---|
| `GET /api/dashboard/summary` | KPIs globaux : total, moyennes, tests du jour |
| `GET /api/dashboard/map` | Points géolocalisés pour la carte |
| `GET /api/dashboard/heatmap` | Points avec intensité pour la carte de chaleur |
| `GET /api/dashboard/operators` | Statistiques agrégées par opérateur |
| `GET /api/dashboard/stats/advanced` | Percentiles du débit (P25, P50, P75, P90) |
| `GET /api/dashboard/timeline?days=30` | Série temporelle quotidienne |
| `GET /api/dashboard/results?page=1&limit=20` | Résultats paginés, filtrables |

Filtres acceptés par `/results` : `operator`, `network`, `from`, `to` (dates au
format `YYYY-MM-DD`).

### Baromètre

`GET /api/barometer?network=4G&operator=Orange&download=12.3&upload=4.1&ping=38`
situe un résultat parmi les mesures abouties des 90 derniers jours, **à type de
connexion égal** (mobile ou Wi-Fi) : part des mesures dépassées en débit
descendant, montant et latence, position parmi les clients du même opérateur,
et débit médian de chaque opérateur ayant au moins 5 mesures. Les variantes
d'un même nom d'opérateur (« TELECEL FASO », « Telecel Faso »…) sont
regroupées. Le calcul est gardé en cache 2 minutes.

### Comptes utilisateurs

Facultatifs : une mesure envoyée sans compte reste anonyme.

| Route | Rôle |
|---|---|
| `POST /api/auth/register` | Crée un compte `{email, password, name}` et renvoie un jeton |
| `POST /api/auth/login` | Ouvre une session `{email, password}` et renvoie un jeton |
| `POST /api/auth/logout` | Révoque le jeton joint |
| `GET /api/auth/me` | Profil et nombre de mesures du compte |
| `PUT /api/auth/me` | Modifie le nom ; le mot de passe sur présentation de l'actuel |
| `DELETE /api/auth/me` | Supprime le compte `{password}` ; ses mesures redeviennent anonymes |
| `GET /api/auth/results` | Mesures du compte, pour reconstruire l'historique de l'application |

Le jeton se joint en en-tête `Authorization: Bearer <jeton>`, y compris à
`POST /results/telemetry` pour rattacher la mesure au compte. C'est un jeton
opaque de 32 octets aléatoires dont seule l'empreinte SHA-256 est stockée
(collection `sessions`, expiration automatique au bout de six mois). Les mots
de passe sont hachés avec bcrypt (collection `users`). Inscription et
connexion sont limitées à 10 tentatives par adresse IP et par quart d'heure.
Le rattachement d'une mesure (`user_id`) n'est jamais exposé par l'API
publique du tableau de bord.

### Proxy vers le service IA

`GET /api/ai/*` est relayé vers `${SPEEDTEST_AI_SERVICE_URL}/ai/*`. Si la
variable est vide, la route n'est pas enregistrée du tout — le service IA est
donc entièrement optionnel.

---

## Comprendre le modèle de données

### Comment les champs mobiles arrivent en base

C'est le point d'architecture le plus important à saisir.

Le schéma de LibreSpeed (`database/schema/schema.go`) ne comporte que douze
champs génériques : ni opérateur, ni GPS, ni technologie radio. Le modifier
aurait cassé les six autres backends de base de données.

L'application mobile fait donc transiter ses champs métier **dans le champ
`extra`**, sous forme de JSON. La fonction `toDocument()` de
`database/mongodb/mongodb.go` déplie ce JSON en colonnes indexées au moment de
l'insertion. C'est une extension propre, sans toucher au moteur amont.

```
Flutter  --->  extra = {"operator":"...","latitude":12.36,"cellularTech":"4G",...}
                            |
                    toDocument() déplie
                            v
MongoDB  --->  { operator: "...", latitude: 12.36, cellular_tech: "4G", ... }
```

### Protection des données personnelles

Les champs `ip_address`, `isp_info`, `user_agent`, `language` et `log` sont
stockés en base mais marqués `json:"-"` dans la structure `Document`. Ils ne
sont **jamais** renvoyés par l'API publique du tableau de bord.

Cette règle est délibérée et doit être préservée : **ajouter un champ personnel
à la sortie JSON exposerait publiquement des données d'utilisateurs**. En cas de
doute, marquer le champ `json:"-"`.

Le corps des requêtes de télémétrie est limité à 64 Kio (`MaxBytesReader`).

### Tests de débit échoués

Un test dont le débit vaut zéro est un test **échoué**, pas une zone sans
couverture. Les agrégations du tableau de bord (moyennes, carte, opérateurs,
percentiles) les écartent via le filtre `speedOnly()` ; ils restent en base et
visibles dans l'historique.

### Convention « zéro = non mesuré »

Pour les tests de streaming et de navigation, une valeur à zéro signifie **test
non effectué**, et non « résultat nul ». Les clients doivent traiter ces deux
cas différemment. Cette convention est respectée de bout en bout par le
tableau de bord web.

---

## Structure du code

```
main.go                      Point d'entrée : config, base, serveur
config/config.go             Chargement settings.toml + variables SPEEDTEST_*
web/
├── web.go                   Routeur chi, CORS, déclaration des routes
├── helpers.go               Génération des données du test de débit
└── getip_util.go            Détection FAI (ipinfo.io, repli GeoIP)
results/
├── telemetry.go             Réception des mesures + image PNG
├── dashboard.go             Handlers HTTP de l'API tableau de bord
├── dashboard_auth.go        Connexion au tableau de bord et contrôle du jeton
├── barometer.go             Baromètre : position d'un résultat
├── accounts.go              Comptes : inscription, sessions, profil
├── stats.go                 Page de statistiques
└── json.go                  Export JSON
database/
├── database.go              Interface commune
├── schema/schema.go         Schéma générique LibreSpeed (12 champs)
├── mongodb/mongodb.go       *** Cœur Yélé : modèle + agrégations ***
├── mongodb/accounts.go      Collections users et sessions
└── mongodb/barometer.go     Lecture des mesures comparées
```

Le fichier à lire en priorité est **`database/mongodb/mongodb.go`** : il contient
le modèle de données complet et toutes les requêtes d'agrégation.

## Tests

```bash
go test ./...
go vet ./...
```

Deux fichiers de tests existent (`results/telemetry_test.go`,
`database/mongodb/mongodb_test.go`). La couverture reste faible : c'est un
chantier ouvert aux contributions.

## Déploiement

### Docker

```bash
docker build -t yele-backend .
docker run -p 8989:8989 \
  -e SPEEDTEST_DATABASE_CONNECTION_STRING="mongodb+srv://..." \
  yele-backend
```

### Render

L'instance de démonstration tourne sur Render en offre gratuite, à l'adresse
`https://mobiletest-j0c6.onrender.com`.

> **Attention au démarrage à froid.** Sur l'offre gratuite, le service s'endort
> après quinze minutes d'inactivité et met soixante à quatre-vingt-dix secondes
> à se réveiller. Pendant ce laps de temps, Render renvoie sa propre page
> d'erreur 502, **sans les en-têtes CORS du serveur Go**. Le navigateur signale
> alors une « erreur CORS » qui n'en est pas une : c'est un simple délai
> d'attente. Les clients doivent prévoir un délai long et des tentatives
> successives.

CORS est configuré en `AllowedOrigins: ["*"]` dans `web/web.go`. L'accès aux
données du tableau de bord repose sur le jeton, pas sur l'origine de la requête.

---

## Rapport avec le projet amont

Ce dépôt suit LibreSpeed speedtest-go. Les ajouts Yélé sont localisés :

- `database/mongodb/` — entièrement nouveau ;
- `results/dashboard.go` — entièrement nouveau ;
- `web/web.go` — routes du tableau de bord et proxy IA ajoutés ;
- `config/config.go` — clé `ai_service_url` ajoutée.

Le reste est du code amont, à ne modifier qu'avec précaution pour garder la
possibilité de synchroniser les correctifs de sécurité.

La documentation d'origine de LibreSpeed est conservée dans
**`README.librespeed.md`**.

## Licence

**LGPL-3.0**, héritée de LibreSpeed speedtest-go. Voir le fichier `LICENSE`.
Toute redistribution doit respecter cette licence.
