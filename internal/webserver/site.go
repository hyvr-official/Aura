package webserver

import (
	"bytes"
	"net"
	"net/url"
	"regexp"
	"strings"
	"text/template"
)

var (
	domainPattern = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	emailPattern  = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

type Input struct {
	Name      string
	Domains   string
	Upstream  string
	Port      int
	WebSocket bool
	SSL       bool
	Email     string
}

func (in Input) DomainList() []string {
	return strings.FieldsFunc(in.Domains, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
}

func (in *Input) Normalize() {
	in.Name = strings.TrimSpace(in.Name)
	in.Domains = strings.ToLower(strings.Join(in.DomainList(), " "))
	in.Upstream = strings.TrimSpace(in.Upstream)
	in.Email = strings.TrimSpace(in.Email)
	if !in.SSL {
		in.Email = ""
	}
}

func (in Input) Validate() map[string]string {
	errs := map[string]string{}
	switch {
	case in.Name == "":
		errs["Name"] = "Name is required."
	case !ValidName(in.Name):
		errs["Name"] = "Use only letters, numbers, dots, dashes and underscores."
	case Exists(in.Name):
		errs["Name"] = "A site with this name already exists."
	}
	domains := in.DomainList()
	if len(domains) == 0 {
		errs["Domains"] = "At least one domain is required."
	}
	for _, d := range domains {
		if !domainPattern.MatchString(d) {
			errs["Domains"] = "Invalid domain: " + d
			break
		}
	}
	if !ValidUpstream(in.Upstream) {
		errs["Upstream"] = "Enter a valid URL, for example http://127.0.0.1:3000."
	}
	if in.Port < 1 || in.Port > 65535 {
		errs["Port"] = "Port must be between 1 and 65535."
	}
	if in.SSL {
		if in.Port != 80 {
			errs["SSL"] = "SSL needs the site to listen on port 80."
		}
		for _, d := range domains {
			if strings.HasPrefix(d, "*.") || net.ParseIP(d) != nil {
				errs["SSL"] = "SSL needs real domain names, not wildcards or IP addresses."
				break
			}
		}
		if in.Email != "" && !ValidEmail(in.Email) {
			errs["Email"] = "Enter a valid email address."
		}
	}
	return errs
}

func ValidUpstream(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\r\n;{}'\"$") {
		return false
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func ValidEmail(s string) bool {
	return emailPattern.MatchString(s)
}

var configTemplate = template.Must(template.New("site").Parse(`server {
    listen {{.Port}};
    listen [::]:{{.Port}};
    server_name {{.Domains}};

    access_log /var/log/nginx/{{.Name}}.access.log;
    error_log /var/log/nginx/{{.Name}}.error.log;

    client_max_body_size 64m;

    location / {
        proxy_pass {{.Upstream}};
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
{{- if .WebSocket}}
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 86400;
{{- end}}
    }
}
`))

func (in Input) Config() (string, error) {
	var buf bytes.Buffer
	if err := configTemplate.Execute(&buf, in); err != nil {
		return "", err
	}
	return buf.String(), nil
}
