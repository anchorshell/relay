package characterization

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const ModelFormatVersion = 1
const modelMagic = "ASFTV1\x00\x00"

var (
	ErrInvalidModel     = errors.New("invalid AnchorShell fastText model")
	ErrTaxonomyMismatch = errors.New("model taxonomy hash mismatch")
)

type NormalizationSettings struct {
	Lowercase bool `json:"lowercase"`
}

type ModelHeader struct {
	FormatVersion   int                   `json:"format_version"`
	ModelVersion    string                `json:"model_version"`
	TaxonomyVersion string                `json:"taxonomy_version"`
	TaxonomyHash    string                `json:"taxonomy_hash"`
	Kind            string                `json:"kind"`
	FastTextSource  string                `json:"fasttext_source"`
	FastTextCommit  string                `json:"fasttext_commit"`
	Dim             int                   `json:"dim"`
	Bucket          int                   `json:"bucket"`
	MinN            int                   `json:"minn"`
	MaxN            int                   `json:"maxn"`
	WordNgrams      int                   `json:"word_ngrams"`
	Dictionary      []string              `json:"dictionary"`
	Labels          []string              `json:"labels"`
	Thresholds      map[string]float64    `json:"thresholds"`
	Normalization   NormalizationSettings `json:"normalization"`
	InputRows       int                   `json:"input_rows"`
	OutputRows      int                   `json:"output_rows"`
	Quantization    string                `json:"quantization"`
}

type Model struct {
	header ModelHeader
	words  map[string]int
	input  []float32
	output []float32
}

func LoadModel(data []byte) (*Model, error) {
	if len(data) < len(modelMagic)+4+sha256.Size || string(data[:len(modelMagic)]) != modelMagic {
		return nil, ErrInvalidModel
	}
	want := data[len(data)-sha256.Size:]
	got := sha256.Sum256(data[:len(data)-sha256.Size])
	if !bytes.Equal(want, got[:]) {
		return nil, fmt.Errorf("%w: checksum", ErrInvalidModel)
	}
	offset := len(modelMagic)
	headerSize := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
	offset += 4
	if headerSize <= 0 || headerSize > 8*1024*1024 || offset+headerSize > len(data)-sha256.Size {
		return nil, fmt.Errorf("%w: header size", ErrInvalidModel)
	}
	var header ModelHeader
	if err := json.Unmarshal(data[offset:offset+headerSize], &header); err != nil {
		return nil, fmt.Errorf("%w: header", ErrInvalidModel)
	}
	offset += headerSize
	if err := validateModelHeader(header); err != nil {
		return nil, err
	}
	inputCount, ok := safeMatrixCount(header.InputRows, header.Dim)
	if !ok {
		return nil, fmt.Errorf("%w: input dimensions", ErrInvalidModel)
	}
	outputCount, ok := safeMatrixCount(header.OutputRows, header.Dim)
	if !ok {
		return nil, fmt.Errorf("%w: output dimensions", ErrInvalidModel)
	}
	wantBytes := (inputCount + outputCount) * 4
	if wantBytes != len(data)-sha256.Size-offset {
		return nil, fmt.Errorf("%w: matrix size", ErrInvalidModel)
	}
	input := make([]float32, inputCount)
	for i := range input {
		input[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[offset+i*4 : offset+i*4+4]))
		if math.IsNaN(float64(input[i])) || math.IsInf(float64(input[i]), 0) {
			return nil, fmt.Errorf("%w: non-finite input weight", ErrInvalidModel)
		}
	}
	offset += inputCount * 4
	output := make([]float32, outputCount)
	for i := range output {
		output[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[offset+i*4 : offset+i*4+4]))
		if math.IsNaN(float64(output[i])) || math.IsInf(float64(output[i]), 0) {
			return nil, fmt.Errorf("%w: non-finite output weight", ErrInvalidModel)
		}
	}
	words := make(map[string]int, len(header.Dictionary))
	for i, word := range header.Dictionary {
		words[word] = i
	}
	return &Model{header: header, words: words, input: input, output: output}, nil
}

func EncodeModel(header ModelHeader, input, output []float32) ([]byte, error) {
	if err := validateModelHeader(header); err != nil {
		return nil, err
	}
	inputCount, ok := safeMatrixCount(header.InputRows, header.Dim)
	if !ok || inputCount != len(input) {
		return nil, ErrInvalidModel
	}
	outputCount, ok := safeMatrixCount(header.OutputRows, header.Dim)
	if !ok || outputCount != len(output) {
		return nil, ErrInvalidModel
	}
	for _, values := range [][]float32{input, output} {
		for _, value := range values {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return nil, fmt.Errorf("%w: non-finite weight", ErrInvalidModel)
			}
		}
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Grow(len(modelMagic) + 4 + len(headerJSON) + (len(input)+len(output))*4 + sha256.Size)
	out.WriteString(modelMagic)
	_ = binary.Write(&out, binary.LittleEndian, uint32(len(headerJSON)))
	out.Write(headerJSON)
	for _, value := range input {
		_ = binary.Write(&out, binary.LittleEndian, math.Float32bits(value))
	}
	for _, value := range output {
		_ = binary.Write(&out, binary.LittleEndian, math.Float32bits(value))
	}
	sum := sha256.Sum256(out.Bytes())
	out.Write(sum[:])
	return out.Bytes(), nil
}

func validateModelHeader(header ModelHeader) error {
	if header.FormatVersion != ModelFormatVersion || header.ModelVersion == "" || header.TaxonomyVersion == "" {
		return fmt.Errorf("%w: version", ErrInvalidModel)
	}
	if header.TaxonomyHash != TaxonomyHash() {
		return ErrTaxonomyMismatch
	}
	if header.Kind != "action" && header.Kind != "metadata" {
		return fmt.Errorf("%w: kind", ErrInvalidModel)
	}
	if header.Dim <= 0 || header.Dim > 4096 || header.Bucket < 0 || header.Bucket > 20_000_000 || header.MinN < 0 || header.MaxN < header.MinN || header.WordNgrams < 1 || header.WordNgrams > 8 {
		return fmt.Errorf("%w: parameters", ErrInvalidModel)
	}
	if header.InputRows != len(header.Dictionary)+header.Bucket || header.OutputRows != len(header.Labels) || len(header.Labels) == 0 {
		return fmt.Errorf("%w: rows", ErrInvalidModel)
	}
	seen := map[string]bool{}
	for _, word := range header.Dictionary {
		if word == "" || seen["w:"+word] {
			return fmt.Errorf("%w: duplicate dictionary entry", ErrInvalidModel)
		}
		seen["w:"+word] = true
	}
	for _, label := range header.Labels {
		if label == "" || seen["l:"+label] {
			return fmt.Errorf("%w: duplicate label", ErrInvalidModel)
		}
		seen["l:"+label] = true
		normalized := normalizeModelLabel(label)
		if header.Kind == "action" && !ValidAction(normalized) {
			return fmt.Errorf("%w: unknown action label", ErrInvalidModel)
		}
		if header.Kind == "metadata" {
			role, object, objectLabel := strings.Cut(normalized, ":")
			if objectLabel {
				if (role != "target" && role != "output") || !ValidObject(object) {
					return fmt.Errorf("%w: unknown object label", ErrInvalidModel)
				}
			} else if !ValidDomain(normalized) {
				return fmt.Errorf("%w: unknown domain label", ErrInvalidModel)
			}
		}
		value, ok := header.Thresholds[label]
		if !ok || value < 0 || value > 1 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%w: missing threshold", ErrInvalidModel)
		}
	}
	if len(header.Thresholds) != len(header.Labels) {
		return fmt.Errorf("%w: unexpected thresholds", ErrInvalidModel)
	}
	return nil
}

func safeMatrixCount(rows, dim int) (int, bool) {
	if rows <= 0 || dim <= 0 || rows > int(^uint(0)>>1)/dim {
		return 0, false
	}
	count := rows * dim
	if count > 400_000_000 {
		return 0, false
	}
	return count, true
}

func (m *Model) Header() ModelHeader { return m.header }

func (m *Model) Predict(text []byte, topK int) []LabelScore {
	if m == nil || len(text) == 0 {
		return nil
	}
	value := string(text)
	if m.header.Normalization.Lowercase {
		value = strings.ToLower(value)
	}
	features := m.features(value)
	if len(features) == 0 {
		return nil
	}
	hidden := make([]float32, m.header.Dim)
	for _, row := range features {
		if row < 0 || row >= m.header.InputRows {
			continue
		}
		base := row * m.header.Dim
		for d := 0; d < m.header.Dim; d++ {
			hidden[d] += m.input[base+d]
		}
	}
	scale := float32(1) / float32(len(features))
	for d := range hidden {
		hidden[d] *= scale
	}
	scores := make([]LabelScore, len(m.header.Labels))
	for row, label := range m.header.Labels {
		base := row * m.header.Dim
		// fastText's `real` is float32 and its dot product accumulates in that
		// type. Preserve that rounding so exported models have measurable parity
		// with the pinned reference implementation.
		dot := float32(0)
		for d := 0; d < m.header.Dim; d++ {
			dot += m.output[base+d] * hidden[d]
		}
		probability := fastTextSigmoid(dot)
		scores[row] = LabelScore{Label: label, Score: probability}
	}
	stableScores(scores)
	if topK > 0 && len(scores) > topK {
		scores = scores[:topK]
	}
	return scores
}

func (m *Model) features(text string) []int {
	words := tokenizeFastText(text)
	features := make([]int, 0, len(words)*8)
	// fastText stores its unsigned word hash in an int32 vector before word
	// n-gram expansion. Values above MaxInt32 are therefore sign-extended when
	// promoted to uint64; retaining uint32 here subtly changes many bigrams.
	hashes := make([]int32, 0, len(words))
	for _, word := range words {
		hashes = append(hashes, int32(fastTextHash([]byte(word))))
		if id, ok := m.words[word]; ok {
			features = append(features, id)
		}
		if m.header.Bucket > 0 && m.header.MaxN > 0 {
			features = append(features, m.subwords(word)...)
		}
	}
	if m.header.WordNgrams > 1 && m.header.Bucket > 0 {
		for i := range hashes {
			h := uint64(int64(hashes[i]))
			for j := i + 1; j < len(hashes) && j < i+m.header.WordNgrams; j++ {
				h = h*116049371 + uint64(int64(hashes[j]))
				features = append(features, len(m.header.Dictionary)+int(h%uint64(m.header.Bucket)))
			}
		}
	}
	return features
}

// fastText v0.9.2 evaluates sigmoid through a 512-entry lookup table bounded
// at +/-8. Recomputing the selected entry gives the same float32 result without
// keeping mutable global state.
func fastTextSigmoid(value float32) float64 {
	if value < -8 {
		return 0
	}
	if value > 8 {
		return 1
	}
	index := int64((value + 8) * 512 / 16)
	x := float32(index*16)/512 - 8
	return float64(float32(1 / (1 + float32(math.Exp(float64(-x))))))
}

func tokenizeFastText(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' || r == '\v' || r == '\r' || r == '\f' || r == 0
	})
	if len(fields) > 0 {
		fields = append(fields, "</s>")
	}
	return fields
}

func (m *Model) subwords(word string) []int {
	if word == "</s>" {
		return nil
	}
	wrapped := []byte("<" + word + ">")
	starts := make([]int, 0, utf8.RuneCount(wrapped)+1)
	for i := 0; i < len(wrapped); {
		starts = append(starts, i)
		_, size := utf8.DecodeRune(wrapped[i:])
		if size <= 0 {
			size = 1
		}
		i += size
	}
	starts = append(starts, len(wrapped))
	out := make([]int, 0, len(starts)*2)
	for i := 0; i < len(starts)-1; i++ {
		for n := 1; n <= m.header.MaxN && i+n < len(starts); n++ {
			if n < m.header.MinN {
				continue
			}
			// Match fastText's boundary exclusions: '<' alone and '>' alone are
			// not useful subwords.
			if n == 1 && (i == 0 || i+n == len(starts)-1) {
				continue
			}
			ngram := wrapped[starts[i]:starts[i+n]]
			row := len(m.header.Dictionary) + int(fastTextHash(ngram)%uint32(m.header.Bucket))
			out = append(out, row)
		}
	}
	return out
}

func fastTextHash(value []byte) uint32 {
	h := uint32(2166136261)
	for _, c := range value {
		// fastText v0.9.2 deliberately sign-extends each byte for backward
		// compatibility with models trained by its original signed-char hash.
		h ^= uint32(int32(int8(c)))
		h *= 16777619
	}
	return h
}

type FastTextClassifier struct {
	action, metadata *Model
	version          string
}

func NewFastTextClassifier(actionBundle, metadataBundle []byte) (*FastTextClassifier, error) {
	action, err := LoadModel(actionBundle)
	if err != nil {
		return nil, err
	}
	metadata, err := LoadModel(metadataBundle)
	if err != nil {
		return nil, err
	}
	if action.header.Kind != "action" || metadata.header.Kind != "metadata" {
		return nil, ErrInvalidModel
	}
	version := action.header.ModelVersion
	if metadata.header.ModelVersion != version {
		version += "+" + metadata.header.ModelVersion
	}
	return &FastTextClassifier{action: action, metadata: metadata, version: version}, nil
}

func (f *FastTextClassifier) Name() string { return "fasttext" }
func (f *FastTextClassifier) Version() string {
	if f == nil {
		return ""
	}
	return f.version
}

func (f *FastTextClassifier) Predict(ctx context.Context, input CandidateInput) (CandidatePrediction, error) {
	if f == nil || f.action == nil || f.metadata == nil {
		return CandidatePrediction{}, ErrInvalidModel
	}
	if err := ctx.Err(); err != nil {
		return CandidatePrediction{}, err
	}
	started := time.Now()
	actions := f.action.Predict(input.Text, 8)
	if err := ctx.Err(); err != nil {
		return CandidatePrediction{}, err
	}
	metadata := f.metadata.Predict(input.Text, 24)
	out := CandidatePrediction{ActionScores: actions, Duration: time.Since(started)}
	for _, score := range metadata {
		label := normalizeModelLabel(score.Label)
		if strings.HasPrefix(label, "target:") || strings.HasPrefix(label, "output:") {
			out.ObjectScores = append(out.ObjectScores, LabelScore{label, score.Score})
		} else if ValidDomain(label) {
			out.DomainScores = append(out.DomainScores, LabelScore{label, score.Score})
		}
	}
	stableScores(out.ObjectScores)
	stableScores(out.DomainScores)
	return out, nil
}

func modelThresholds(models ...*Model) map[string]float64 {
	out := map[string]float64{}
	for _, model := range models {
		if model == nil {
			continue
		}
		for label, value := range model.header.Thresholds {
			out[normalizeThresholdLabel(label)] = value
		}
	}
	return out
}

func normalizeThresholdLabel(label string) string {
	normalized := normalizeModelLabel(label)
	if strings.HasPrefix(label, "__label__action_") {
		return "action:" + normalized
	}
	if ValidDomain(normalized) {
		return "domain:" + normalized
	}
	return normalized
}

func (f *FastTextClassifier) Thresholds() map[string]float64 {
	if f == nil {
		return nil
	}
	return modelThresholds(f.action, f.metadata)
}

var _ CandidateClassifier = (*FastTextClassifier)(nil)

// deterministic ordering guard used by format tests and exporters.
func sortedThresholdKeys(values map[string]float64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
