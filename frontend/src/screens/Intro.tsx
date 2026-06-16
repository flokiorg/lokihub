import { EmblaCarouselType } from "embla-carousel";
import {
  ArrowRightIcon,
  CloudLightningIcon,
  LucideIcon,
  ShieldCheckIcon,
  WalletIcon,
} from "lucide-react";
import { motion } from "motion/react";
import React, { ReactElement, useState } from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { CompactLanguageSwitcher } from "src/components/CompactLanguageSwitcher";
import { Button } from "src/components/ui/button";
import {
  Carousel,
  CarouselApi,
  CarouselContent,
  CarouselItem,
} from "src/components/ui/carousel";
import {
  CarouselDotButton,
  CarouselDots,
} from "src/components/ui/custom/carousel-dots";
import { useDotButton } from "src/components/ui/custom/useDotButton";
import { useTheme } from "src/components/ui/theme-provider";
import { useInfo } from "src/hooks/useInfo";
import { cn } from "src/lib/utils";

interface Star {
  id: number;
  x: number;
  y: number;
  size: number;
  opacity: number;
  duration: number;
  delay: number;
}

export function StarryNight({ children }: { children: React.ReactNode }) {
  const [stars] = useState<Star[]>(() => {
    const starCount = 1200;
    const newStars: Star[] = [];

    for (let i = 0; i < starCount; i++) {
      newStars.push({
        id: i,
        x: Math.random() * 140 - 20,
        y: Math.random() * 140 - 20,
        size: Math.random() * 3 + 1,
        opacity: Math.random() * 0.7 + 0.3,
        duration: Math.random() * 20 + 15,
        delay: Math.random() * 10,
      });
    }
    return newStars;
  });

  const [shootingStars] = useState<
    { id: number; delay: number; repeatDelay: number; top: number; left: number }[]
  >(() => {
    const shootingStarCount = 4;
    const newShootingStars = [];
    for (let i = 0; i < shootingStarCount; i++) {
      newShootingStars.push({
        id: i,
        // Shorter initial delay, between 0 and 10 seconds
        delay: Math.random() * 10,
        // Varied repeat delay between 5 and 15 - shorter than before
        repeatDelay: Math.random() * 10 + 5,
        // Start from random positions in the top-left quadrant
        top: Math.random() * 30,
        left: Math.random() * 40,
      });
    }
    return newShootingStars;
  });

  return (
    <div className="relative w-full h-screen bg-background overflow-hidden">
      {/* Rotating stars container */}
      <motion.div
        className="absolute inset-0"
        style={{
          transformOrigin: "center center",
        }}
        animate={{
          rotate: 360,
        }}
        transition={{
          duration: 120, // 2 minutes for full rotation
          repeat: Infinity,
          ease: "linear",
        }}
      >
        {stars.map((star) => (
          <motion.div
            key={star.id}
            className="absolute rounded-full bg-white"
            style={{
              left: `${star.x}%`,
              top: `${star.y}%`,
              width: `${star.size}px`,
              height: `${star.size}px`,
              boxShadow: `0 0 ${star.size * 2}px rgba(255, 255, 255, 0.5)`,
            }}
            initial={{
              opacity: star.opacity,
            }}
            animate={{
              opacity: [star.opacity, star.opacity * 0.3, star.opacity],
            }}
            transition={{
              duration: star.duration * 0.3,
              delay: star.delay,
              repeat: Infinity,
              repeatType: "reverse",
              ease: "easeInOut",
            }}
          />
        ))}
      </motion.div>

      {/* Add some shooting stars */}
      {shootingStars.map((star) => (
        <motion.div
          key={`shooting-star-${star.id}`}
          className="absolute w-1 h-1 bg-white rounded-full"
          style={{
            left: `${star.left}%`,
            top: `${star.top}%`,
            boxShadow: "0 0 10px rgba(255, 255, 255, 0.8)",
          }}
          initial={{
            x: -100,
            y: -100,
            opacity: 0,
            scale: 0,
          }}
          animate={{
            x: window.innerWidth + 100,
            y: window.innerHeight + 100,
            opacity: [0, 1, 1, 0],
            scale: [0, 1, 1, 0],
          }}
          transition={{
            duration: 3,
            delay: star.delay,
            repeat: Infinity,
            repeatDelay: star.repeatDelay,
            ease: "easeOut",
          }}
        />
      ))}

      {/* Content Overlay */}
      <div className="absolute inset-0 z-10">{children}</div>
    </div>
  );
}

export function Intro() {
  const { data: info } = useInfo(true); // poll so we auto-redirect if setupCompleted becomes true
  const navigate = useNavigate();
  const { t } = useTranslation("setup");
  const [api, setApi] = React.useState<CarouselApi>();
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const [_, setProgress] = React.useState<number>(0);
  const { selectedIndex, scrollSnaps, onDotButtonClick } = useDotButton(api);
  const { setDarkMode } = useTheme();

  React.useEffect(() => {
    // Force dark mode on intro screen
    setDarkMode("dark");
    return () => {
      // Revert to default after exiting intro
      setDarkMode("system");
    };
  }, [setDarkMode]);

  React.useEffect(() => {
    if (!info?.setupCompleted) {
      return;
    }
    navigate("/");
  }, [info, navigate]);

  React.useEffect(() => {
    api?.on("scroll", (x) => {
      setProgress(x.scrollProgress());
    });
  }, [api]);

  return (
    <>
    {createPortal(
      <div dir="ltr" className="fixed top-4 right-4 z-[9999]">
        <CompactLanguageSwitcher showLabel />
      </div>,
      document.body
    )}
    <StarryNight>
      <Carousel className={cn("w-full h-full bg-transparent")} setApi={setApi}>
        <CarouselContent className="select-none bg-transparent">
          <CarouselItem>
            <div className="flex flex-col justify-center items-center h-screen p-5">
              <div className="flex flex-col gap-4 text-center items-center max-w-lg">
                <div className="text-4xl font-extrabold text-foreground">
                  {t("intro.slide1.title")}
                </div>
                <div className="text-2xl text-muted-foreground font-semibold">
                  {t("intro.slide1.subtitle")}
                </div>
                <div className="mt-20 flex flex-col items-center gap-3">
                  <Button onClick={() => api?.scrollNext()} size="lg">
                    {t("intro.slide1.next")}
                  </Button>
                </div>
              </div>
            </div>
          </CarouselItem>
          <CarouselItem>
            <Slide
              api={api}
              icon={CloudLightningIcon}
              title={t("intro.slide2.title")}
              description={t("intro.slide2.description")}
            />
          </CarouselItem>
          <CarouselItem>
            <Slide
              api={api}
              icon={ShieldCheckIcon}
              title={t("intro.slide3.title")}
              description={t("intro.slide3.description")}
            />
          </CarouselItem>
          <CarouselItem>
            <Slide
              api={api}
              icon={WalletIcon}
              title={t("intro.slide4.title")}
              description={t("intro.slide4.description")}
            />
          </CarouselItem>
        </CarouselContent>
        <div className="absolute bottom-5 left-1/2 -translate-x-1/2">
          <CarouselDots>
            {scrollSnaps.map((_, index) => (
              <CarouselDotButton
                key={index}
                data-selected={index === selectedIndex}
                onClick={() => onDotButtonClick(index)}
                aria-label={`Go to slide ${index + 1}`}
              />
            ))}
          </CarouselDots>
        </div>
      </Carousel>
    </StarryNight>
    </>
  );
}

function Slide({
  api,
  title,
  description,
  icon: Icon,
}: {
  api: EmblaCarouselType | undefined;
  title: string;
  description: string;
  icon: LucideIcon;
  button?: ReactElement;
}) {
  const navigate = useNavigate();

  const slideNext = function () {
    if (api?.canScrollNext()) {
      api.scrollNext();
    } else {
      navigate("/setup");
    }
  };

  return (
    <div className="flex flex-col justify-center items-center h-screen gap-8 p-5">
      <Icon className="w-16 h-16 text-primary-background" />
      <div className="flex flex-col gap-4 text-center items-center max-w-lg">
        <div className="text-3xl font-semibold text-primary-background">
          {title}
        </div>
        <div className="text-lg text-muted-foreground font-semibold">
          {description}
        </div>
      </div>
      <Button size="icon" onClick={slideNext} className="">
        <ArrowRightIcon className="size-4 rtl:rotate-180" />
      </Button>
    </div>
  );
}
