package glance

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"time"
)

var (
	videosBilibiliWidgetTemplate             = mustParseTemplate("bilibili.html", "widget-base.html", "bilibili-card-contents.html")
	videosBilibiliWidgetGridTemplate         = mustParseTemplate("bilibili-grid.html", "widget-base.html", "bilibili-card-contents.html")
)

type videosBilibiliWidget struct {
	widgetBase        `yaml:",inline"`
	Videos            bilibiliVideoList `yaml:"-"`
	VideoUrlTemplate  string    `yaml:"video-url-template"`
	Style             string    `yaml:"style"`
	CollapseAfter     int       `yaml:"collapse-after"`
	CollapseAfterRows int       `yaml:"collapse-after-rows"`
	Classify          []string  `yaml:"classify"`
	Limit             int       `yaml:"limit"`
	IncludeShorts     bool      `yaml:"include-shorts"`
}

func (widget *videosBilibiliWidget) initialize() error {
	widget.withTitle("Videos").withCacheDuration(time.Hour)

	if widget.Limit <= 0 {
		widget.Limit = 25
	}

	if widget.CollapseAfterRows == 0 || widget.CollapseAfterRows < -1 {
		widget.CollapseAfterRows = 4
	}

	if widget.CollapseAfter == 0 || widget.CollapseAfter < -1 {
		widget.CollapseAfter = 7
	}

	return nil
}

func (widget *videosBilibiliWidget) update(ctx context.Context) {
	videos, err := fetchBilibiliClassifyUploads(widget.Classify, widget.VideoUrlTemplate, widget.IncludeShorts)

	if !widget.canContinueUpdateAfterHandlingErr(err) {
		return
	}

	if len(videos) > widget.Limit {
		videos = videos[:widget.Limit]
	}

	widget.Videos = videos
}

func (widget *videosBilibiliWidget) Render() template.HTML {
	var template *template.Template

	switch widget.Style {
	case "grid-cards":
		template = videosBilibiliWidgetGridTemplate
	default:
		template = videosBilibiliWidgetTemplate
	}

	return widget.renderTemplate(widget, template)
}

type bilibiliFeedResponseJson struct {
	Data struct {
		List []struct {
			Pic   string `json:"pic"`    // 封面图URL
			Title string `json:"title"`  // 视频标题
			Bvid  string `json:"bvid"`   // 视频唯一标识
			Owner struct {
				Mid  int64  `json:"mid"`  // UP主ID
				Name string `json:"name"` // UP主名称
			} `json:"owner"`
			Stats struct {
				View int64 `json:"view"` // 播放量
				Danmaku int64 `json:"danmaku"` // 弹幕数
			} `json:"stat"`
		} `json:"list"`
	} `json:"data"`
}

func getBilibiliFeedURL(classify string) string {
	wts := time.Now().Unix()
	switch classify {
	case "all":
		return fmt.Sprintf("https://api.bilibili.com/x/web-interface/popular?ps=20&pn=1&wts=%d", wts)
	case "weekly":
		start := time.Date(2019, time.March, 22, 0, 0, 0, 0, time.UTC)
		now := time.Now().UTC()
		duration := now.Sub(start)
		days := int(duration.Hours() / 24)
		period := days/7
		return fmt.Sprintf("https://api.bilibili.com/x/web-interface/popular/series/one?number=%d&wts=%d", period, wts)
	case "history":
		return fmt.Sprintf("https://api.bilibili.com/x/web-interface/popular/precious?page_size=100&page=1&wts=%d", wts)
	default:
		return fmt.Sprintf("https://api.bilibili.com/x/web-interface/ranking/v2?rid=0&type=all&wts=%d", wts)
	}
}

type videoBilibili struct {
	ThumbnailUrl string
	Title        string
	Url          string
	Author       string
	AuthorUrl    string
	Views        int64
	Danmaku		 int64
	Desc		 string
}

type bilibiliVideoList []videoBilibili

func (v bilibiliVideoList) sortByView() bilibiliVideoList {
	sort.Slice(v, func(i, j int) bool {
		return v[i].Views > v[j].Views
	})

	return v
}

//all:https://api.bilibili.com/x/web-interface/popular?ps=20&pn=1&wts=1742301430
//weekly:https://api.bilibili.com/x/web-interface/popular/series/one?number=312&wts=1742227080
//history: https://api.bilibili.com/x/web-interface/popular/precious?page_size=100&page=1&wts=1742301563
//rank/all: https://api.bilibili.com/x/web-interface/ranking/v2?rid=0&type=all&wts=1742301628
func fetchBilibiliClassifyUploads(classify []string, videoUrlTemplate string, includeShorts bool) (bilibiliVideoList, error) {
	requests := make([]*http.Request, 0, len(classify))

	for i := range classify {
		feedUrl := getBilibiliFeedURL(classify[i])
		request, _ := http.NewRequest("GET", feedUrl, nil)
		request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
		request.Header.Set("Referer", "https://www.bilibili.com/")
		requests = append(requests, request)
	}

	job := newJob(decodeJsonFromRequestTask[bilibiliFeedResponseJson](defaultHTTPClient), requests).withWorkers(30)
	responses, errs, err := workerPoolDo(job)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoContent, err)
	}

	videos := make(bilibiliVideoList, 0, len(classify)*15)
	var failed int

	for i := range responses {
		if errs[i] != nil {
			failed++
			slog.Error("Failed to fetch bilibi", "classify", classify[i], "error", errs[i])
			continue
		}

		response := responses[i]

		for j := range response.Data.List {
			v := &response.Data.List[j]

			videos = append(videos, videoBilibili{
				ThumbnailUrl: v.Pic,
				Title:        v.Title,
				Url:          fmt.Sprintf("https://www.bilibili.com/video/%s", v.Bvid),
				Author:       v.Owner.Name,
				AuthorUrl:    fmt.Sprintf("https://space.bilibili.com/%d", v.Owner.Mid),
				Views:        v.Stats.View,
				Danmaku:        v.Stats.Danmaku,
			})
		}
	}

	if len(videos) == 0 {
		return nil, errNoContent
	}

	videos.sortByView()

	if failed > 0 {
		return videos, fmt.Errorf("%w: missing videos from %d channels", errPartialContent, failed)
	}

	return videos, nil
}
