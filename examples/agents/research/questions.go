package research

const (
	wp = "https://en.wikipedia.org/wiki/"
	wv = "https://en.wikivoyage.org/wiki/"
)

// questions are the eval. Every fact an answer needs is in the snapshots,
// and support lists the pages that state it. An answer that takes a sum,
// a difference or a comparison is stated nowhere, so its support lists the
// pages its parts come from. A conflict question's archive page gives an
// older figure, so it isn't in support.
var questions = []question{
	{"The engineer who built the Sule Skerry lighthouse had an uncle who designed a shelter for meteorological instruments. What is that shelter called?",
		[]string{"Stevenson screen"}, []string{wp + "Thomas_Stevenson"}, twoHop, ""},
	{"The engineer who built the Sule Skerry lighthouse had a daughter who became a best-selling author. Under what name did she publish?",
		[]string{"D. E. Stevenson", "D E Stevenson", "DE Stevenson"}, []string{wp + "David_Alan_Stevenson"}, twoHop, ""},
	{"The writer who visited Muckle Flugga Lighthouse in 1869 had a cousin whose daughter became a best-selling author. She published more than how many books?",
		[]string{"40", "forty"}, []string{wp + "David_Alan_Stevenson"}, twoHop, ""},
	{"Bressay Lighthouse was designed by two brothers. In what year did the one who first said a lighthouse in Shetland waters was impossible die?",
		[]string{"1886"}, []string{wp + "David_Stevenson_(engineer)"}, twoHop, ""},
	{"One Shetland island group's airstrip once had the world's shortest runway for commercial flights. From which Mainland village does its usual ferry sail?",
		[]string{"Vidlin"}, []string{wv + "Out_Skerries"}, twoHop, ""},
	{"Noup Head Lighthouse in Orkney was built by an engineer who retired in his eighties. In what year did he retire?",
		[]string{"1938"}, []string{wp + "David_Alan_Stevenson"}, twoHop, ""},
	{"Charles Alexander Stevenson worked unpaid in the family firm until his uncle died. How old was that uncle when he died?",
		[]string{"68", "sixty-eight"}, []string{wp + "Thomas_Stevenson", wp + "Charles_Alexander_Stevenson"}, count, ""},
	{"How old was David Stevenson, the engineer, on the day Muckle Flugga Lighthouse was first lit?",
		[]string{"42", "forty-two"}, []string{wp + "David_Stevenson_(engineer)", wp + "Muckle_Flugga_Lighthouse"}, count, ""},
	{"How many years separate the building of Sumburgh Head Lighthouse and the first lighting of Muckle Flugga Lighthouse?",
		[]string{"37", "thirty-seven"}, []string{wp + "Sumburgh_Head_Lighthouse", wp + "Muckle_Flugga_Lighthouse"}, count, ""},
	{"Which of David Stevenson's two lighthouse-engineer sons built more lighthouses?",
		[]string{"David Alan Stevenson", "David Alan", "David A. Stevenson"}, []string{wp + "David_Alan_Stevenson", wp + "Charles_Alexander_Stevenson"}, compare, "Charles"},
	{"For how many years had the Sule Skerry Ringing Group been monitoring seabirds when the Out Skerries air service was axed?",
		[]string{"39", "thirty-nine"}, []string{wp + "Sule_Skerry", wv + "Out_Skerries"}, count, ""},
	{"How many minutes longer is the ferry crossing to Foula than the flight to Foula?",
		[]string{"105"}, []string{wv + "Foula"}, count, ""},
	{"The Fair Isle ferry was built in St Monans and entered service in one year. Funding for its replacement was awarded in another. How many years apart were they?",
		[]string{"37", "thirty-seven"}, []string{wp + "Good_Shepherd_IV"}, count, ""},
	{"For how many years was the Sule Skerry lighthouse manned, from its opening to its automation?",
		[]string{"87", "eighty-seven"}, []string{wp + "Sule_Skerry"}, count, ""},
	{"A Shetland lighthouse was renamed in 1964, and its keepers' old shore station now hosts a nature reserve's visitor centre. What was the lighthouse called before 1964?",
		[]string{"North Unst"}, []string{wp + "Muckle_Flugga_Lighthouse"}, lookup, ""},
	{"Which was automated first, Sumburgh Head Lighthouse or Muckle Flugga Lighthouse?",
		[]string{"Sumburgh Head"}, []string{wp + "Sumburgh_Head_Lighthouse", wp + "Muckle_Flugga_Lighthouse"}, compare, "Muckle Flugga"},
	{"What was the population of Foula at the most recent census?",
		[]string{"17", "seventeen"}, []string{wp + "Foula"}, conflict, ""},
	{"How many permanent residents did Fair Isle have at the most recent census?",
		[]string{"44", "forty-four"}, []string{wp + "Fair_Isle"}, conflict, ""},
	{"By how many people did the population of Unst change between the 2011 and 2022 censuses?",
		[]string{"12", "twelve"}, []string{wp + "Unst"}, count, ""},
	{"By how many people did Foula's population fall between the 2011 and 2022 censuses?",
		[]string{"13", "thirteen"}, []string{wp + "Foula"}, count, ""},
}
