# evaluation-service (Go)

Este é o serviço de avaliação, o "caminho quente" (hot path) do projeto ToggleMaster. É o único endpoint que os clientes finais (ex: seu app mobile, seu site) devem chamar.

Ele é otimizado para alta velocidade e baixa latência usando **cache em Redis**.

Ele funciona da seguinte forma:

1. Recebe uma requisição (`/evaluate?user_id=...&flag_name=...`).
2. Busca as regras da flag no **Redis**.
3. **Se não estiver no cache (Cache MISS):**
   - Busca a definição da flag no `flag-service`.
   - Busca a regra no `targeting-service`.
   - Salva o resultado no Redis com um TTL (Time-To-Live) curto.
4. Executa a lógica de avaliação (ex: "o usuário está nos 50%?").
5. Retorna `true` ou `false` para o cliente.
6. Envia *assincronamente* um evento da decisão para uma fila **AWS SQS**.

## 📦 Pré-requisitos (Local)

- Go (versão 1.21 ou superior)
- Redis (rodando localmente ou em Docker)
- Os serviços `auth-service`, `flag-service` e `targeting-service` devem estar rodando.
- **Credenciais da AWS:** para o SQS funcionar, seu terminal deve estar autenticado na AWS (ex: via `aws configure` ou variáveis de ambiente).

## 🚀 Rodando Localmente

1. **Clone o repositório** e entre na pasta `evaluation-service`.

2. **Crie uma Chave de API de Serviço:**

   Este serviço precisa se autenticar no `flag-service` e no `targeting-service`. Você deve criar uma chave de API para ele usando o `auth-service` (com a `MASTER_KEY`).

   ```bash
   curl -X POST http://localhost:8001/admin/keys \
     -H "Content-Type: application/json" \
     -H "Authorization: Bearer admin-secreto-123" \
     -d '{"name": "evaluation-service-key"}'
   ```

   Guarde a chave `key` retornada (ex: `tm_key_...`). Vamos chamá-la de `SUA_CHAVE_DE_SERVICO`.

3. **Configure as Variáveis de Ambiente:**

   Crie um arquivo chamado `.env` na raiz desta pasta com o seguinte conteúdo:

   ```env
   PORT="8004"

   REDIS_URL="redis://localhost:6379"

   FLAG_SERVICE_URL="http://localhost:8002"
   TARGETING_SERVICE_URL="http://localhost:8003"

   SERVICE_API_KEY="SUA_CHAVE_DE_SERVICO"

   AWS_SQS_URL="https://sqs.us-east-1.amazonaws.com/123456789012/sua-fila"
   AWS_REGION="us-east-1"
   ```

4. **Instale as Dependências:**

   ```bash
   go mod tidy
   ```

5. **Inicie o Serviço:**

   ```bash
   go run .
   ```

   O servidor estará rodando em `http://localhost:8004`.

## 🧪 Testando os Endpoints

Para os testes, vamos assumir que você já criou:

1. Uma flag chamada `enable-new-dashboard` no `flag-service`.
2. Uma regra para `enable-new-dashboard` no `targeting-service` do tipo `PERCENTAGE` com valor `50`.

### 1. Verifique a Saúde (Health Check)

```bash
curl http://localhost:8004/health
```

Saída esperada:

```json
{"status":"ok"}
```

### 2. Teste a Avaliação

Tente alguns IDs de usuário diferentes. O hash determinístico fará com que alguns caiam dentro dos 50% e outros fora.

```bash
curl "http://localhost:8004/evaluate?user_id=user-123&flag_name=enable-new-dashboard"
```

Saída de exemplo:

```json
{"flag_name":"enable-new-dashboard","user_id":"user-123","result":true}
```

Outro exemplo:

```bash
curl "http://localhost:8004/evaluate?user_id=user-abc&flag_name=enable-new-dashboard"
```

Saída de exemplo:

```json
{"flag_name":"enable-new-dashboard","user_id":"user-abc","result":false}
```

### 3. Verifique o Cache

Execute o mesmo comando duas vezes seguidas. Na segunda execução, o serviço deverá utilizar o resultado armazenado no Redis.

### 4. Verifique a Fila SQS

Após realizar as chamadas acima, acesse a fila SQS configurada e verifique o recebimento dos eventos de avaliação.

---

# 🔄 CI/CD e DevSecOps

Para a Fase 3 do Tech Challenge, o `evaluation-service` foi integrado a uma pipeline de CI utilizando **GitHub Actions**, com uma reusable workflow compartilhada pelo projeto.

A pipeline é acionada em Pull Requests, execução manual (`workflow_dispatch`) e pushes na branch `aws-cloud-k8s`.

O fluxo implementado contempla:

- Build da aplicação;
- Execução dos testes;
- Lint e validação de formatação;
- SCA (Software Composition Analysis) com Trivy;
- SAST (Static Application Security Testing) com Gosec;
- Security Gate;
- Build da imagem Docker;
- Scan da imagem Docker com Trivy;
- Autenticação na AWS utilizando GitHub Actions OIDC;
- Login no Amazon ECR;
- Push da imagem aprovada para o ECR.

A reusable workflow utilizada é:

```text
ramondata/CI-reusable-source/.github/workflows/ci-reusable.yml@v1.5.0
```

## ⚙️ Variáveis da pipeline

A pipeline utiliza Repository Variables configuradas no GitHub.

| Variável | Descrição |
| --- | --- |
| `AWS_REGION` | Região AWS utilizada pelo projeto |
| `AWS_ROLE_ARN` | ARN da IAM Role assumida pelo GitHub Actions |
| `ECR_REPOSITORY` | Repositório ECR de destino |
| `APP_VERSION` | Versão utilizada na geração da tag da imagem |

Para o Evaluation Service:

```text
ECR_REPOSITORY=toggle-master/evaluation-service
```

A imagem publicada no ECR recebe uma tag formada pela versão da aplicação e pelo SHA reduzido do commit.

Exemplo:

```text
v1.0.0-abcdef1
```

Nenhuma Access Key ou Secret Access Key da AWS precisa ser armazenada no repositório. A autenticação é realizada através de **OIDC**.

## 🐳 Docker

O serviço utiliza **multi-stage build**.

No primeiro estágio, uma imagem Go é utilizada para compilar a aplicação. No segundo, é utilizada uma imagem Alpine contendo somente os componentes necessários para executar o binário.

A configuração atual utiliza:

```text
Builder: golang:1.25.7-alpine
Runtime: alpine:3.22
```

O build do binário é realizado com:

```text
CGO_ENABLED=0
GOOS=linux
```

Essa abordagem reduz os componentes presentes na imagem final e, consequentemente, sua superfície de ataque.

## 🔐 Autenticação GitHub Actions → AWS

A integração entre GitHub Actions e AWS utiliza **OpenID Connect (OIDC)**.

O workflow assume uma IAM Role específica para o serviço:

```text
evaluation-service-github-ecr
```

A role possui as permissões necessárias para publicação da imagem no repositório:

```text
toggle-master/evaluation-service
```

A Trust Policy da role restringe a identidade autorizada a assumir a role ao repositório e branch utilizados pela pipeline.

## 🛡️ Validações de segurança

A pipeline possui verificações de segurança antes da publicação da imagem.

### SAST

O **Gosec** realiza análise estática do código Go.

Problemas encontrados nessa etapa precisam ser corrigidos antes que o código continue pelo Security Gate.

### SCA

O **Trivy** analisa as dependências do projeto em busca de vulnerabilidades conhecidas.

### Docker Image Scan

Após a criação da imagem Docker, o Trivy executa uma nova análise sobre a imagem.

Vulnerabilidades críticas bloqueiam a continuidade da pipeline e impedem que uma imagem vulnerável seja publicada no ECR.

## 🚧 Problemas encontrados e soluções

Durante a implementação da pipeline foram encontrados alguns problemas importantes.

### Falha nas validações do Gosec

Nas primeiras execuções, o Gosec identificou problemas no código Go.

Foram realizados ajustes no código para atender às validações de segurança antes de prosseguir para as etapas seguintes.

Esse comportamento também permitiu validar o funcionamento do Security Gate, garantindo que problemas bloqueantes interrompam a pipeline.

### Vulnerabilidades críticas na imagem Docker

O scan da imagem Docker com Trivy identificou vulnerabilidades críticas relacionadas às imagens base utilizadas inicialmente.

O Dockerfile foi atualizado para versões mais recentes de Go e Alpine:

```text
golang:1.25.7-alpine
alpine:3.22
```

Após a atualização, o scan da imagem deixou de bloquear a pipeline por essas vulnerabilidades.

### Falha na autenticação OIDC

Durante a autenticação com a AWS, a pipeline apresentou o erro:

```text
Not authorized to perform sts:AssumeRoleWithWebIdentity
```

A causa estava na Trust Policy da IAM Role: a condição `sub` não correspondia à identidade enviada pelo repositório atual.

A Trust Policy foi atualizada para utilizar a identidade correta do repositório e da branch que executam a pipeline.

Após a correção, o GitHub Actions conseguiu assumir a IAM Role e prosseguir para a autenticação no Amazon ECR.

## ✅ Resultado

Após os ajustes, o fluxo de CI do `evaluation-service` passou a executar:

```text
Código
  ↓
Build e testes
  ↓
Lint
  ↓
SCA - Trivy
  ↓
SAST - Gosec
  ↓
Security Gate
  ↓
Docker Build
  ↓
Trivy Image Scan
  ↓
AWS OIDC
  ↓
Amazon ECR
```

Somente após a aprovação das validações da pipeline a imagem é publicada no repositório:

```text
toggle-master/evaluation-service
```

A publicação no ECR conclui a etapa de **CI e geração do artefato** deste serviço.