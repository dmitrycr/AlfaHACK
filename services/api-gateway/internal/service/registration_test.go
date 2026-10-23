package service

import (
	"strings"
	"testing"
)

func TestRegistrationRequiresEmployeeFieldsWithoutFinancialData(t *testing.T) {
	valid := RegistrationInput{Email: " EMPLOYEE@example.test ", FullName: " Иван Иванов ", JobTitle: "Финансовый директор", Password: "Registration-Test-2026!", CompanyName: "Тест Металл Альфа", INN: "9900000017"}
	normalized, e := normalizeRegistration(valid)
	if e != nil || normalized.Email != "employee@example.test" || normalized.FullName != "Иван Иванов" {
		t.Fatal(e)
	}
	for _, change := range []func(*RegistrationInput){
		func(in *RegistrationInput) { in.FullName = "" },
		func(in *RegistrationInput) { in.JobTitle = "" },
		func(in *RegistrationInput) { in.CompanyName = "" },
		func(in *RegistrationInput) { in.Email = "Somebody <employee@example.test>" },
		func(in *RegistrationInput) { in.Password = "short" },
		func(in *RegistrationInput) { in.Password = strings.Repeat("я", 37) },
		func(in *RegistrationInput) { in.FullName = "Иван\x00Иванов" },
	} {
		in := valid
		change(&in)
		if _, e := normalizeRegistration(in); e == nil {
			t.Fatal("invalid registration accepted")
		}
	}
}
