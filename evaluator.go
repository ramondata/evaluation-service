package main

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// Tempo de vida do cache em segundos
	CACHE_TTL = 30 * time.Second
)

var validFlagName = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// getDecision é o wrapper principal
func (a *App) getDecision(ctx context.Context, userID, flagName string) (bool, error) {
	// 1. Obter os dados da flag (do cache ou dos serviços)
	info, err := a.getCombinedFlagInfo(ctx, flagName)
	if err != nil {
		return false, err
	}

	// 2. Executar a lógica de avaliação
	return a.runEvaluationLogic(info, userID), nil
}

// getCombinedFlagInfo busca os dados no Redis, com fallback para os microsserviços
func (a *App) getCombinedFlagInfo(ctx context.Context, flagName string) (*CombinedFlagInfo, error) {
	cacheKey := fmt.Sprintf("flag_info:%s", flagName)

	// 1. Tentar buscar do Cache (Redis)
	val, err := a.RedisClient.Get(ctx, cacheKey).Result()
	if err == nil {
		// Cache HIT
		var info CombinedFlagInfo

		if err := json.Unmarshal([]byte(val), &info); err == nil {
			log.Printf("Cache HIT para flag '%s'", flagName)
			return &info, nil
		}

		// Se o unmarshal falhar, trata como cache miss
		log.Printf(
			"Erro ao desserializar cache para flag '%s': %v",
			flagName,
			err,
		)
	}

	log.Printf("Cache MISS para flag '%s'", flagName)

	// 2. Cache MISS - Buscar dos serviços
	info, err := a.fetchFromServices(ctx, flagName)
	if err != nil {
		return nil, err
	}

	// 3. Salvar no Cache
	jsonData, err := json.Marshal(info)
	if err == nil {
		if err := a.RedisClient.Set(
			ctx,
			cacheKey,
			jsonData,
			CACHE_TTL,
		).Err(); err != nil {
			log.Printf(
				"Erro ao salvar flag '%s' no cache: %v",
				flagName,
				err,
			)
		}
	}

	return info, nil
}

// fetchFromServices busca dados do flag-service e targeting-service concorrentemente
func (a *App) fetchFromServices(
	ctx context.Context,
	flagName string,
) (*CombinedFlagInfo, error) {
	var wg sync.WaitGroup

	wg.Add(2)

	var flagInfo *Flag
	var ruleInfo *TargetingRule
	var flagErr error
	var ruleErr error

	// Goroutine 1: Buscar do flag-service
	go func() {
		defer wg.Done()

		flagInfo, flagErr = a.fetchFlag(ctx, flagName)
	}()

	// Goroutine 2: Buscar do targeting-service
	go func() {
		defer wg.Done()

		ruleInfo, ruleErr = a.fetchRule(ctx, flagName)
	}()

	wg.Wait()

	if flagErr != nil {
		return nil, flagErr
	}

	if ruleErr != nil {
		log.Printf(
			"Aviso: Nenhuma regra de segmentação encontrada para '%s'. Usando padrão.",
			flagName,
		)
	}

	return &CombinedFlagInfo{
		Flag: flagInfo,
		Rule: ruleInfo,
	}, nil
}

// buildServiceURL valida os dados usados para montar URLs internas.
func buildServiceURL(
	baseURL string,
	resource string,
	flagName string,
) (string, error) {
	if !validFlagName.MatchString(flagName) {
		return "", fmt.Errorf("flagName contém caracteres inválidos")
	}

	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("URL base inválida: %w", err)
	}

	if parsedBaseURL.Scheme != "http" &&
		parsedBaseURL.Scheme != "https" {
		return "", fmt.Errorf(
			"protocolo inválido na URL base: %s",
			parsedBaseURL.Scheme,
		)
	}

	if parsedBaseURL.Host == "" {
		return "", fmt.Errorf("host ausente na URL base")
	}

	escapedFlagName := url.PathEscape(flagName)

	parsedBaseURL.Path = strings.TrimRight(
		parsedBaseURL.Path,
		"/",
	) + "/" + resource + "/" + escapedFlagName

	return parsedBaseURL.String(), nil
}

// fetchFlag busca os dados da flag no flag-service
func (a *App) fetchFlag(ctx context.Context, flagName string) (*Flag, error) {
	requestURL, err := buildServiceURL(
		a.FlagServiceURL,
		"flags",
		flagName,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"erro ao montar URL do flag-service: %w",
			err,
		)
	}

	apiKey := os.Getenv("SERVICE_API_KEY")

	// #nosec G704 -- requestURL usa host configurado internamente e flagName validado.
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		requestURL,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"erro ao criar request para flag-service: %w",
			err,
		)
	}

	req.Header.Set(
		"Authorization",
		"Bearer "+apiKey,
	)

	// #nosec G704 -- destino foi validado por buildServiceURL antes da requisição.
	resp, err := a.HttpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf(
			"erro ao chamar flag-service: %w",
			err,
		)
	}

	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf(
				"Erro ao fechar response body do flag-service: %v",
				err,
			)
		}
	}()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &NotFoundError{flagName}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"flag-service retornou status %d",
			resp.StatusCode,
		)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf(
			"erro ao ler resposta do flag-service: %w",
			err,
		)
	}

	var flag Flag

	if err := json.Unmarshal(body, &flag); err != nil {
		return nil, fmt.Errorf(
			"erro ao desserializar resposta do flag-service: %w",
			err,
		)
	}

	return &flag, nil
}

// fetchRule busca os dados da regra no targeting-service
func (a *App) fetchRule(ctx context.Context, flagName string) (*TargetingRule, error) {
	requestURL, err := buildServiceURL(
		a.TargetingServiceURL,
		"rules",
		flagName,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"erro ao montar URL do targeting-service: %w",
			err,
		)
	}

	apiKey := os.Getenv("SERVICE_API_KEY")

	// #nosec G704 -- requestURL usa host configurado internamente e flagName validado.
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		requestURL,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"erro ao criar request para targeting-service: %w",
			err,
		)
	}

	req.Header.Set(
		"Authorization",
		"Bearer "+apiKey,
	)

	// #nosec G704 -- destino foi validado por buildServiceURL antes da requisição.
	resp, err := a.HttpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf(
			"erro ao chamar targeting-service: %w",
			err,
		)
	}

	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf(
				"Erro ao fechar response body do targeting-service: %v",
				err,
			)
		}
	}()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &NotFoundError{flagName}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"targeting-service retornou status %d",
			resp.StatusCode,
		)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf(
			"erro ao ler resposta do targeting-service: %w",
			err,
		)
	}

	var rule TargetingRule

	if err := json.Unmarshal(body, &rule); err != nil {
		return nil, fmt.Errorf(
			"erro ao desserializar resposta do targeting-service: %w",
			err,
		)
	}

	return &rule, nil
}

// runEvaluationLogic é onde a decisão é tomada
func (a *App) runEvaluationLogic(
	info *CombinedFlagInfo,
	userID string,
) bool {
	if info.Flag == nil || !info.Flag.IsEnabled {
		return false
	}

	if info.Rule == nil || !info.Rule.IsEnabled {
		return true
	}

	// 3. Processa a regra (só temos "PERCENTAGE" por enquanto)
	rule := info.Rule.Rules

	if rule.Type == "PERCENTAGE" {
		// Converte o 'value' (que é interface{}) para float64
		percentage, ok := rule.Value.(float64)
		if !ok {
			log.Printf(
				"Erro: valor da regra de porcentagem não é um número para a flag '%s'",
				info.Flag.Name,
			)

			return false
		}

		// Calcula o "bucket" do usuário (0-99)
		userBucket := getDeterministicBucket(
			userID + info.Flag.Name,
		)

		if float64(userBucket) < percentage {
			return true
		}
	}

	return false
}

func getDeterministicBucket(input string) int {
	// Usamos SHA1 (rápido) e pegamos os primeiros 4 bytes
	hasher := sha1.New()

	if _, err := hasher.Write([]byte(input)); err != nil {
		log.Printf(
			"Erro ao gerar hash: %v",
			err,
		)

		return 0
	}

	hash := hasher.Sum(nil)

	// Converte 4 bytes para um uint32
	val := binary.BigEndian.Uint32(hash[:4])

	// Retorna o módulo 100
	return int(val % 100)
}
