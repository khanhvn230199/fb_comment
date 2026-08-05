package controller

import (
	"reflect"
	"testing"
)

func TestFacebookPostIDCandidatesFromURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "permalink story id",
			raw:  "https://www.facebook.com/permalink.php?story_fbid=222&id=111",
			want: []string{"111_222", "222"},
		},
		{
			name: "group posts",
			raw:  "https://www.facebook.com/groups/111/posts/222",
			want: []string{"111_222", "222"},
		},
		{
			name: "group permalink",
			raw:  "https://www.facebook.com/groups/111/permalink/222",
			want: []string{"111_222", "222"},
		},
		{
			name: "watch query",
			raw:  "https://www.facebook.com/watch/?v=333",
			want: []string{"333"},
		},
		{
			name: "reel",
			raw:  "https://www.facebook.com/reel/444",
			want: []string{"444"},
		},
		{
			name: "short fb watch no direct candidate",
			raw:  "https://fb.watch/abc",
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := facebookPostIDCandidatesFromURL(tt.raw)
			if got == nil {
				got = []string{}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("facebookPostIDCandidatesFromURL() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestGraphPostTotalLikeCount(t *testing.T) {
	withReactions := FacebookGraphPostResponse{
		Reactions: FacebookGraphSummaryEdge{Summary: FacebookGraphSummary{TotalCount: 34}},
		Likes:     FacebookGraphSummaryEdge{Summary: FacebookGraphSummary{TotalCount: 9}},
	}
	if got := graphPostTotalLikeCount(withReactions); got != 34 {
		t.Fatalf("graphPostTotalLikeCount() = %d, want 34", got)
	}

	withLikesOnly := FacebookGraphPostResponse{
		Likes: FacebookGraphSummaryEdge{Summary: FacebookGraphSummary{TotalCount: 9}},
	}
	if got := graphPostTotalLikeCount(withLikesOnly); got != 9 {
		t.Fatalf("graphPostTotalLikeCount() = %d, want 9", got)
	}
}
