import { ExternalLinkIcon } from "lucide-react";
import { useTranslation } from "react-i18next";
import AppHeader from "src/components/AppHeader";
import ExternalLink from "src/components/ExternalLink";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "src/components/ui/accordion";
import { Button } from "src/components/ui/button";

import ReactMarkdown from "react-markdown";

import Loading from "src/components/Loading";
import { useFAQ } from "src/hooks/useFAQ";

export function FAQ() {
  const { t } = useTranslation("help");
  const { faq, isLoading } = useFAQ();

  if (isLoading) {
    return <Loading />;
  }

  return (
    <div className="grid gap-5">
      <AppHeader
        title={t("faq.title")}
        description={t("faq.description")}
      />
      <div className="max-w-2xl" dir="ltr">
        <Accordion type="single" collapsible className="w-full">
          {faq?.map((item, index) => (
            <AccordionItem key={index} value={`item-${index}`}>
              <AccordionTrigger>{item.question}</AccordionTrigger>
              <AccordionContent>
                <div className="prose dark:prose-invert text-muted-foreground text-sm">
                  <ReactMarkdown
                    components={{
                      a: ({ node, className, children, ...props }) => (
                        <ExternalLink
                          to={props.href as string}
                          className="underline inline-flex items-center gap-1"
                        >
                          {children}
                          <ExternalLinkIcon className="h-3 w-3" />
                        </ExternalLink>
                      ),
                    }}
                  >
                    {item.answer}
                  </ReactMarkdown>
                </div>
              </AccordionContent>
            </AccordionItem>
          ))}
        </Accordion>

        <div className="mt-8 p-4 border rounded-lg bg-muted/30">
          <h3 className="font-semibold mb-2">{t("faq.stillHaveQuestions")}</h3>
          <p className="text-sm text-muted-foreground mb-4">
            {t("faq.communityDesc")}
          </p>
          <ExternalLink to="https://flokicoin.org/discord">
            <Button>
              {t("faq.joinDiscord")}
              <ExternalLinkIcon className="ms-2 h-4 w-4" />
            </Button>
          </ExternalLink>
        </div>
      </div>
    </div>
  );
}
