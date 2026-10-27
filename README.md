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

## CI/CD no AWS Academy

O pipeline da branch produtiva `aws-cloud-k8s` autentica com o repository secret
`AWS_ACADEMY_CREDENTIALS`. Cole o bloco de **AWS Details → AWS CLI**, com
`aws_access_key_id`, `aws_secret_access_key` e `aws_session_token`.
O cabeçalho `[default]` é opcional. O secret é configurado neste repositório;
o secret do auth-service não é compartilhado automaticamente.

O workflow usa `ramondata/CI-reusable-source`, fixado por SHA, no modo Academy.
As credenciais são validadas e mascaradas, sem assumir uma role via OIDC.
O código-fonte e a imagem passam pelas verificações antes da publicação no ECR.
Pull requests executam somente as verificações, sem autenticar na AWS.

| Variável opcional | Valor padrão |
| --- | --- |
| `AWS_REGION` | `us-east-1` |
| `ECR_REPOSITORY` | `toggle-master/evaluation-service` |
| `APP_VERSION` | `v1.0.0` |

O ECR deve existir na conta e região do laboratório. A tag publicada segue
`APP_VERSION-SHA_CURTO`, por exemplo `v1.0.0-abcdef1`.

A cada reinício do laboratório, substitua o bloco inteiro do secret e use
**Re-run all jobs** numa execução por push que já contenha esta alteração.
Alterar o secret não dispara o pipeline. O botão **Run workflow** depende de
este arquivo também existir na branch padrão do repositório.

### GitOps automático

Após a publicação da imagem, o pipeline atualiza automaticamente o repositório
existente `ramondata/toggle-master-gitops`. Configure `GITOPS_DEPLOY_KEY` neste
repositório com uma chave privada autorizada a escrever no GitOps.
Os pushes fazem rebase e até três tentativas para acomodar atualizações de
outros serviços na mesma branch sem usar force push.
O job atualiza repositório e tag em `apps/evaluation-service/values.yaml`, na
`master`. A configuração de ArgoCD e do cluster é uma etapa separada.

A verificação de lock exige que `go.mod` e `go.sum` estejam sincronizados.
O workflow duplicado `first-ci-evaluation.yml` foi removido para evitar duas
publicações e tentativas de autenticação diferentes no mesmo push.
